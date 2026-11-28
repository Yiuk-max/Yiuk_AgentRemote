#include "mainwindow.h"

#include "nativeops.h"
#include "vision/apppaths.h"

#include "powershellrunner.h"


QJsonObject MainWindow::localToolSuccessResult(const QJsonValue &data) {
    QJsonObject result{{"ok", true}, {"code", "OK"}};
    if (!data.isUndefined()) result.insert("data", data);
    return result;
}


QJsonObject MainWindow::localToolErrorResult(const QString &code, const QString &message) {
    return {{"ok", false}, {"code", code}, {"message", message}};
}


QJsonObject MainWindow::deviceRecord() const {
    const bool platformConnected = platform && platform->isConnected();
    return {{"device_id", deviceId()}, {"name", QSysInfo::machineHostName()},
            {"online", platformConnected}, {"local_client_running", bridge && bridge->isListening()},
            {"platform_connected", platformConnected}, {"platform", "windows"},
            {"backend", platformConnected ? "platform" : "local-demo"}};
}


QJsonObject MainWindow::localListDevices(const QJsonObject &) {
    return localToolSuccessResult(QJsonArray{deviceRecord()});
}

QJsonObject MainWindow::localDeviceStatus(const QJsonObject &input) {
    if (input.value("device_id").toString() != deviceId()) return localToolErrorResult("DEVICE_NOT_FOUND", "只能连接当前设备");
    QJsonObject status = deviceRecord();
    status.insert("active_sessions", sessions.size());
    return localToolSuccessResult(status);
}

QJsonObject MainWindow::localCloseSession(const QJsonObject &input) {
    return closeSessionAndStopTasks(input.value("session_id").toString());
}

// 本机 bridge 的工具分发。走表而不是 if 链：新增工具只要加一行，
// 而成员函数指针的错误（名字打错、签名不符）在编译期就会报出来。
QJsonObject MainWindow::invokeLocalTool(const QString &tool, const QJsonObject &input) {
    if (!bridge || !bridge->isListening()) return localToolErrorResult("LOCAL_CLIENT_UNAVAILABLE", "本机 Qt 客户端桥接未启动");

    using LocalTool = QJsonObject (MainWindow::*)(const QJsonObject &);
    static const QHash<QString, LocalTool> tools = [] {
        QHash<QString, LocalTool> table;
        table.insert(QStringLiteral("list_devices"), &MainWindow::localListDevices);
        table.insert(QStringLiteral("get_device_status"), &MainWindow::localDeviceStatus);
        table.insert(QStringLiteral("request_connection"), &MainWindow::createLocalSessionRequest);
        table.insert(QStringLiteral("close_session"), &MainWindow::localCloseSession);
        table.insert(QStringLiteral("execute_command"), &MainWindow::startLocalCommand);
        table.insert(QStringLiteral("get_command_result"), &MainWindow::localCommandResultFromCursor);
        table.insert(QStringLiteral("cancel_command"), &MainWindow::cancelLocalCommand);
        table.insert(QStringLiteral("download_file"), &MainWindow::downloadLocalFile);
        table.insert(QStringLiteral("upload_file"), &MainWindow::uploadLocalFile);
        table.insert(QStringLiteral("ask_user"), &MainWindow::askLocalUser);
        table.insert(QStringLiteral("get_user_reply"), &MainWindow::getLocalUserReply);
        return table;
    }();

    // get_session_status 只看参数里的 session_id，与其它工具的参数形态不同，
    // 单独适配一下，表的签名才能保持统一。
    if (tool == QStringLiteral("get_session_status")) {
        return localSessionStatusResult(input.value("session_id").toString());
    }
    const LocalTool handler = tools.value(tool, nullptr);
    if (handler == nullptr) return localToolErrorResult("UNKNOWN_TOOL", "未支持的工具");
    return (this->*handler)(input);
}


QJsonObject MainWindow::createLocalSessionRequest(const QJsonObject &input) {
    const QString targetDeviceId = input.value("device_id").toString();
    if (targetDeviceId != deviceId()) return localToolErrorResult("DEVICE_NOT_FOUND", "只能连接当前设备");
    if (hasOtherLiveSession()) return localToolErrorResult("SESSION_LIMIT_REACHED", "当前已有一个活动或待批准会话");
    const QString sessionId = QUuid::createUuid().toString(QUuid::WithoutBraces);
    const QDateTime createdAt = QDateTime::currentDateTimeUtc();
    const QDateTime expiresAt = createdAt.addSecs(600);
    QJsonObject session{{"session_id", sessionId}, {"device_id", targetDeviceId}, {"purpose", input.value("purpose").toString()},
                        {"permissions", QJsonArray{"execute_command", "read_file", "write_file"}}, {"status", "pending_approval"},
                        {"requester", "Local MCP process (unverified)"}, {"source_device", QSysInfo::machineHostName() + " · this PC"},
                        {"created_at", createdAt.toString(Qt::ISODate)},
                        {"expires_at", expiresAt.toString(Qt::ISODate)}, {"expires_at_ms", expiresAt.toMSecsSinceEpoch()}};
    sessions.insert(sessionId, session);
    persistSessionState();
    refreshPendingRequests();
    refreshSessionsPage();
    showStatusMessage("有待批准的协助请求", 4000);
    QTimer::singleShot(600000, this, [this, sessionId] { expireSessionIfNeeded(sessionId); });
    notificationSessions.insert(sessionId, input.value("purpose").toString());
    if (trayIcon && QSystemTrayIcon::supportsMessages()) {
        trayIcon->showMessage("需要批准远程协助", input.value("purpose").toString() + "\n点击此通知查看并批准或拒绝。",
                              QSystemTrayIcon::Information, 10000);
    } else {
        QTimer::singleShot(0, this, [this, sessionId] { showApprovalDialog(sessionId); });
    }
    return localToolSuccessResult(session);
}


QJsonObject MainWindow::localSessionStatusResult(const QString &sessionId) {
    if (!sessions.contains(sessionId)) return localToolErrorResult("SESSION_EXPIRED", "会话不存在或已过期");
    expireSessionIfNeeded(sessionId);
    return localToolSuccessResult(sessions.value(sessionId));
}


QJsonObject MainWindow::downloadLocalFile(const QJsonObject &input) {
    const QString sessionId = input.value("session_id").toString();
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return localToolErrorResult("SESSION_EXPIRED", "会话不存在或已过期");
    const QJsonObject session = sessions.value(sessionId);
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved") return localToolErrorResult("PERMISSION_DENIED", "设备所有者尚未批准此会话");
    bool canRead = false;
    for (const QJsonValue &permission : session.value("permissions").toArray()) {
        if (permission.toString() == QLatin1String("read_file")) canRead = true;
    }
    if (!canRead) return localToolErrorResult("PERMISSION_DENIED", "会话未获得 read_file 权限");
    const QString remotePath = input.value("remote_path").toString().trimmed();
    if (remotePath.isEmpty() || remotePath.size() > 4096) return localToolErrorResult("INVALID_ARGUMENT", "远程路径为空或过长");
    QFileInfo remoteInfo(remotePath);
    if (!remoteInfo.exists() || !remoteInfo.isFile()) return localToolErrorResult("FILE_NOT_FOUND", "远程路径不是可读取的普通文件");
    constexpr qint64 maxDownloadBytes = 32LL * 1024 * 1024;
    if (remoteInfo.size() > maxDownloadBytes) return localToolErrorResult("FILE_TOO_LARGE", "文件超过 32 MiB 下载上限");
    QString destination = input.value("destination_path").toString().trimmed();
    if (!destination.isEmpty()) {
        if (!QFileInfo(destination).isAbsolute()) return localToolErrorResult("INVALID_ARGUMENT", "destination_path 必须是绝对路径");
        if (QFileInfo::exists(destination)) return localToolErrorResult("LOCAL_PATH_EXISTS", "destination_path 已存在，拒绝覆盖");
    } else {
        const QString generatedDestinationPath =
            QDir(yiuk::vision::programDataPath(QStringLiteral("downloads")))
                .filePath(QString("%1-%2").arg(QDateTime::currentMSecsSinceEpoch()).arg(remoteInfo.fileName()));
        destination = generatedDestinationPath;
    }
    if (!QDir().mkpath(QFileInfo(destination).absolutePath())) return localToolErrorResult("LOCAL_WRITE_FAILED", "无法创建下载目录");
    QFile sourceFile(remotePath);
    if (!sourceFile.open(QIODevice::ReadOnly)) return localToolErrorResult("FILE_NOT_FOUND", "无法打开远程文件");
    QSaveFile destinationFile(destination);
    if (!destinationFile.open(QIODevice::WriteOnly)) return localToolErrorResult("LOCAL_WRITE_FAILED", "无法创建本地文件");
    QCryptographicHash checksum(QCryptographicHash::Sha256);
    qint64 copiedBytes = 0;
    while (!sourceFile.atEnd()) {
        const QByteArray chunk = sourceFile.read(64 * 1024);
        if (chunk.isEmpty() && sourceFile.error() != QFile::NoError) {
            destinationFile.cancelWriting();
            return localToolErrorResult("FILE_READ_FAILED", "读取远程文件失败");
        }
        if (destinationFile.write(chunk) != chunk.size()) {
            destinationFile.cancelWriting();
            return localToolErrorResult("LOCAL_WRITE_FAILED", "写入本地文件失败");
        }
        checksum.addData(chunk);
        copiedBytes += chunk.size();
    }
    if (remoteInfo.size() >= 0 && copiedBytes != remoteInfo.size()) {
        destinationFile.cancelWriting();
        return localToolErrorResult("FILE_INTEGRITY_FAILED", "读取文件大小与预期不一致");
    }
    if (!destinationFile.commit()) return localToolErrorResult("LOCAL_WRITE_FAILED", "提交本地文件失败");
    return localToolSuccessResult(QJsonObject{{"session_id", sessionId}, {"remote_path", remotePath},
                          {"file_name", remoteInfo.fileName()}, {"local_path", destination},
                          {"size", static_cast<double>(copiedBytes)},
                          {"sha256", QString::fromLatin1(checksum.result().toHex())},
                          {"status", "succeeded"}});
}


// uploadLocalFile 将本地文件复制到本机的 uploads 目录
// 本地桥接没有远程设备，因此源文件和目标文件位于同一台主机
QJsonObject MainWindow::uploadLocalFile(const QJsonObject &input) {
    const QString sessionId = input.value("session_id").toString();
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return localToolErrorResult("SESSION_EXPIRED", "会话不存在或已过期");
    const QJsonObject session = sessions.value(sessionId);
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved") return localToolErrorResult("PERMISSION_DENIED", "设备所有者尚未批准此会话");
    bool canWrite = false;
    for (const QJsonValue &permission : session.value("permissions").toArray()) {
        if (permission.toString() == QLatin1String("write_file")) canWrite = true;
    }
    if (!canWrite) return localToolErrorResult("PERMISSION_DENIED", "会话未获得 write_file 权限");
    const QString localPath = input.value("local_path").toString().trimmed();
    if (localPath.isEmpty() || !QFileInfo(localPath).isAbsolute()) {
        return localToolErrorResult("INVALID_ARGUMENT", "local_path 必须是绝对路径");
    }
    QFileInfo localInfo(localPath);
    if (!localInfo.exists() || !localInfo.isFile()) return localToolErrorResult("LOCAL_READ_FAILED", "local_path 不是可读取的普通文件");
    constexpr qint64 maxUploadBytes = 32LL * 1024 * 1024;
    if (localInfo.size() > maxUploadBytes) return localToolErrorResult("FILE_TOO_LARGE", "文件超过 32 MiB 上传上限");
    const QString requestedPath = input.value("remote_path").toString().trimmed();
    QString targetPath;
    if (requestedPath.isEmpty()) {
        targetPath = QDir(yiukConfigRoot())
            .filePath(QString("uploads/%1-%2").arg(QDateTime::currentMSecsSinceEpoch()).arg(localInfo.fileName()));
    } else {
        if (!QFileInfo(requestedPath).isAbsolute()) return localToolErrorResult("INVALID_ARGUMENT", "remote_path 必须是绝对路径");
        QFileInfo requestedInfo(requestedPath);
        targetPath = requestedInfo.isDir() ? QDir(requestedPath).filePath(localInfo.fileName()) : requestedPath;
    }
    const bool overwrite = input.value("overwrite").toBool(false);
    if (QFileInfo::exists(targetPath) && !overwrite) return localToolErrorResult("REMOTE_PATH_EXISTS", "目标文件已存在；如需覆盖请设置 overwrite=true");
    if (!QDir().mkpath(QFileInfo(targetPath).absolutePath())) return localToolErrorResult("LOCAL_WRITE_FAILED", "无法创建目标目录");
    QFile sourceFile(localPath);
    if (!sourceFile.open(QIODevice::ReadOnly)) return localToolErrorResult("LOCAL_READ_FAILED", "无法打开 local_path");
    QSaveFile destinationFile(targetPath);
    if (!destinationFile.open(QIODevice::WriteOnly)) return localToolErrorResult("LOCAL_WRITE_FAILED", "无法创建目标文件");
    QCryptographicHash checksum(QCryptographicHash::Sha256);
    qint64 copiedBytes = 0;
    while (!sourceFile.atEnd()) {
        const QByteArray chunk = sourceFile.read(64 * 1024);
        if (chunk.isEmpty() && sourceFile.error() != QFile::NoError) {
            destinationFile.cancelWriting();
            return localToolErrorResult("LOCAL_READ_FAILED", "读取 local_path 失败");
        }
        if (destinationFile.write(chunk) != chunk.size()) {
            destinationFile.cancelWriting();
            return localToolErrorResult("LOCAL_WRITE_FAILED", "写入目标文件失败");
        }
        checksum.addData(chunk);
        copiedBytes += chunk.size();
    }
    if (copiedBytes != localInfo.size()) {
        destinationFile.cancelWriting();
        return localToolErrorResult("FILE_INTEGRITY_FAILED", "读取文件大小与预期不一致");
    }
    if (!destinationFile.commit()) return localToolErrorResult("LOCAL_WRITE_FAILED", "提交目标文件失败");
    return localToolSuccessResult(QJsonObject{{"session_id", sessionId}, {"local_path", localPath},
                          {"remote_path", targetPath}, {"file_name", QFileInfo(targetPath).fileName()},
                          {"size", static_cast<double>(copiedBytes)},
                          {"sha256", QString::fromLatin1(checksum.result().toHex())},
                          {"overwrite", overwrite}, {"status", "succeeded"}});
}


QJsonObject MainWindow::startLocalCommand(const QJsonObject &input) {
    const QString sessionId = input.value("session_id").toString();
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return localToolErrorResult("SESSION_EXPIRED", "会话不存在或已过期");
    if (sessions.value(sessionId).value("status").toString() != "active") return localToolErrorResult("PERMISSION_DENIED", "设备所有者尚未批准此会话");
    const QString command = input.value("command").toString();
    if (command.trimmed().isEmpty() || command.size() > 32768) return localToolErrorResult("INVALID_ARGUMENT", "命令为空或超过 32 KiB");
    const QString requestId = QUuid::createUuid().toString(QUuid::WithoutBraces);
    const int timeoutSeconds = qBound(1, input.value("timeout_seconds").toInt(60), 3600);
    const QString requestedWorkingDirectory = input.value("working_directory").toString().trimmed();
    const QString workingDirectory = requestedWorkingDirectory.isEmpty()
        ? defaultCommandWorkingDirectory() : requestedWorkingDirectory;

    tasks.insert(requestId, {{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"},
                             {"stdout", ""}, {"stderr", ""}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}});

    PowerShellRunner::Callbacks callbacks;
    callbacks.onOutput = [this, requestId](const QByteArray &retainedOutput, bool isStandardError, bool truncated) {
        QJsonObject commandTask = tasks.value(requestId);
        const QString outputField = isStandardError ? QStringLiteral("stderr") : QStringLiteral("stdout");
        commandTask.insert(outputField, commandTask.value(outputField).toString() + QString::fromUtf8(retainedOutput));
        if (truncated) commandTask.insert("truncated", true);
        tasks.insert(requestId, commandTask);
    };
    callbacks.onFinished = [this, requestId](const QString &status, int exitCode, const QString &error) {
        if (PowerShellRunner *runner = commandRunners.take(requestId)) runner->deleteLater();
        QJsonObject commandTask = tasks.value(requestId);
        if (status == QLatin1String("timed_out")) {
            commandTask.insert("status", "timed_out");
            tasks.insert(requestId, commandTask);
        } else if (status != QLatin1String("cancelled")) {
            if (!error.isEmpty()) {
                commandTask.insert("status", "failed");
                commandTask.insert("stderr", commandTask.value("stderr").toString() + error);
            } else {
                commandTask.insert("status", status);
                commandTask.insert("exit_code", exitCode);
            }
            tasks.insert(requestId, commandTask);
        }
        persistSessionState();
        refreshSessionsPage();
        refreshActiveSessions();
        if (status == QLatin1String("timed_out"))
            showStatusMessage("PowerShell 任务已超时", 4000);
        else if (status != QLatin1String("cancelled"))
            showStatusMessage("PowerShell 任务完成", 3000);
    };

    auto *runner = new PowerShellRunner(this, requestId, command, workingDirectory, timeoutSeconds, callbacks);
    commandRunners.insert(requestId, runner);
    QString startError;
    if (!runner->start(&startError)) {
        commandRunners.remove(requestId);
        runner->deleteLater();
        tasks.remove(requestId);
        return localToolErrorResult("EXECUTION_FAILED", startError);
    }
    showStatusMessage("正在执行本机 PowerShell 任务", 3000);
    refreshActiveSessions();
    return localToolSuccessResult(QJsonObject{{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"}});
}


QJsonObject MainWindow::localCommandResultFromCursor(const QJsonObject &input) {
    const QString requestId = input.value("request_id").toString();
    if (!tasks.contains(requestId) || tasks.value(requestId).value("session_id").toString() != input.value("session_id").toString())
        return localToolErrorResult("TASK_NOT_FOUND", "任务不存在或不属于该会话");
    QJsonObject result = tasks.value(requestId);
    const int cursor = qMax(0, input.value("cursor").toInt());
    result.insert("stdout", result.value("stdout").toString().mid(cursor));
    return localToolSuccessResult(result);
}


QJsonObject MainWindow::cancelLocalCommand(const QJsonObject &input) {
    const QString requestId = input.value("request_id").toString();
    if (!tasks.contains(requestId) || tasks.value(requestId).value("session_id").toString() != input.value("session_id").toString())
        return localToolErrorResult("TASK_NOT_FOUND", "任务不存在或不属于该会话");
    QJsonObject commandTask = tasks.value(requestId);
    commandTask.insert("status", "cancelled");
    tasks.insert(requestId, commandTask);
    if (PowerShellRunner *runner = commandRunners.value(requestId)) runner->cancel();
    persistSessionState();
    refreshSessionsPage();
    return localToolSuccessResult(QJsonObject{{"request_id", requestId}, {"status", "cancelled"}});
}

// /native：本机桥的原生操作入口。
//
// 与平台链路共用同一个 NativeOperationDispatcher（同样的注册表、取消、超时、信封上限），
// 差别只有三处：请求来自 loopback+token 的本地 HTTP、会话来自本地会话表、结果异步回包。
// 校验顺序与平台一致：会话存在 → 已批准 → 能力存在 → 参数/去重交给 dispatcher。
void MainWindow::runLocalNative(const QJsonObject &request,
                                const std::function<void(const QJsonObject &)> &respond) {
    const auto fail = [&respond](const QString &code, const QString &message, const QString &hint = QString()) {
        QJsonObject envelope{{"ok", false}, {"code", code}, {"message", message}};
        if (!hint.isEmpty()) envelope.insert("hint", hint);
        respond(envelope);
    };
    if (!nativeOps) return fail(QStringLiteral("LOCAL_CLIENT_UNAVAILABLE"), QStringLiteral("原生操作不可用"));

    const QString sessionId = request.value("session_id").toString();
    const QString operation = request.value("operation").toString();
    if (sessionId.isEmpty() || operation.isEmpty())
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("session_id 与 operation 必填"));

    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId))
        return fail(QStringLiteral("SESSION_EXPIRED"), QStringLiteral("会话不存在或已过期"));
    if (sessions.value(sessionId).value("status").toString() != QLatin1String("active"))
        return fail(QStringLiteral("PERMISSION_DENIED"), QStringLiteral("设备所有者尚未批准此会话"));
    if (!nativeOps->supports(operation))
        return fail(QStringLiteral("UNSUPPORTED_OPERATION"),
                    QStringLiteral("客户端未注册操作 %1").arg(operation),
                    QStringLiteral("旧客户端没有这个能力；升级客户端后重试"));

    const QJsonValue argumentsValue = request.value("arguments");
    if (!argumentsValue.isUndefined() && !argumentsValue.isObject())
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("arguments 必须是 JSON 对象"));
    const QJsonObject arguments = argumentsValue.toObject();
    const int timeoutSeconds = qBound(1, request.value("timeout_seconds").toInt(10), 25);
    QString requestId = request.value("request_id").toString();
    if (requestId.isEmpty()) requestId = QUuid::createUuid().toString(QUuid::WithoutBraces);

    // 一次性订阅：拿到这个 request_id 的终态就回包并断开，避免连接堆积。
    auto connection = std::make_shared<QMetaObject::Connection>();
    *connection = connect(
        nativeOps, &NativeOperationDispatcher::finished, this,
        [respond, connection, requestId](const QString &, const QString &finishedId, const QString &,
                                         const QJsonObject &envelope) {
            if (finishedId != requestId) return;
            QObject::disconnect(*connection);
            respond(envelope);
        });

    if (!nativeOps->start(sessionId, requestId, operation, arguments, timeoutSeconds)) {
        QObject::disconnect(*connection);
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("原生操作请求无效，或这个 request_id 已经在执行/已完成"));
    }
    appendActivity(sessionId, QStringLiteral("本地 MCP 请求原生操作 %1").arg(operation), ActivityKind::Execute);
}
