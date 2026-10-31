#include "mainwindow.h"

#include "powershellrunner.h"


QJsonObject MainWindow::ok(const QJsonValue &data) {
    QJsonObject result{{"ok", true}, {"code", "OK"}};
    if (!data.isUndefined()) result.insert("data", data);
    return result;
}


QJsonObject MainWindow::fail(const QString &code, const QString &message) {
    return {{"ok", false}, {"code", code}, {"message", message}};
}


QJsonObject MainWindow::deviceRecord() const {
    const bool platformConnected = platform && platform->isConnected();
    return {{"device_id", deviceId()}, {"name", QSysInfo::machineHostName()},
            {"online", platformConnected}, {"local_client_running", bridge && bridge->isListening()},
            {"platform_connected", platformConnected}, {"platform", "windows"},
            {"backend", platformConnected ? "platform" : "local-demo"}};
}


QJsonObject MainWindow::invokeLocalTool(const QString &tool, const QJsonObject &input) {
    if (!bridge || !bridge->isListening()) return fail("LOCAL_CLIENT_UNAVAILABLE", "本机 Qt 客户端桥接未启动");
    if (tool == "list_devices") return ok(QJsonArray{deviceRecord()});
    if (tool == "get_device_status") {
        if (input.value("device_id").toString() != deviceId()) return fail("DEVICE_NOT_FOUND", "只能连接当前设备");
        QJsonObject status = deviceRecord();
        status.insert("active_sessions", sessions.size());
        return ok(status);
    }
    if (tool == "request_connection") return requestLocalSession(input);
    if (tool == "get_session_status") return sessionStatus(input.value("session_id").toString());
    if (tool == "execute_command") return executeLocalCommand(input);
    if (tool == "download_file") return downloadLocalFile(input);
    if (tool == "upload_file") return uploadLocalFile(input);
    if (tool == "get_command_result") return commandResult(input);
    if (tool == "cancel_command") return cancelCommand(input);
    if (tool == "close_session") return closeSession(input.value("session_id").toString());
    return fail("UNKNOWN_TOOL", "未支持的工具");
}


QJsonObject MainWindow::requestLocalSession(const QJsonObject &input) {
    const QString target = input.value("device_id").toString();
    if (target != deviceId()) return fail("DEVICE_NOT_FOUND", "只能连接当前设备");
    const QString id = QUuid::createUuid().toString(QUuid::WithoutBraces);
    const QDateTime createdAt = QDateTime::currentDateTimeUtc();
    const QDateTime expiresAt = createdAt.addSecs(600);
    QJsonObject session{{"session_id", id}, {"device_id", target}, {"purpose", input.value("purpose").toString()},
                        {"permissions", input.value("permissions")}, {"status", "pending_approval"},
                        {"requester", "Local MCP process (unverified)"}, {"source_device", QSysInfo::machineHostName() + " · this PC"},
                        {"created_at", createdAt.toString(Qt::ISODate)},
                        {"expires_at", expiresAt.toString(Qt::ISODate)}, {"expires_at_ms", expiresAt.toMSecsSinceEpoch()}};
    sessions.insert(id, session);
    persistSessionState();
    refreshPendingRequests();
    refreshSessionsPage();
    showStatusMessage("有待批准的协助请求", 4000);
    QTimer::singleShot(600000, this, [this, id] { expireSessionIfNeeded(id); });
    notificationSessions.insert(id, input.value("purpose").toString());
    if (trayIcon && QSystemTrayIcon::supportsMessages()) {
        trayIcon->showMessage("需要批准远程协助", input.value("purpose").toString() + "\n点击此通知查看并批准或拒绝。",
                              QSystemTrayIcon::Information, 10000);
    } else {
        QTimer::singleShot(0, this, [this, id] { showApprovalDialog(id); });
    }
    return ok(session);
}


QJsonObject MainWindow::sessionStatus(const QString &id) {
    if (!sessions.contains(id)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
    expireSessionIfNeeded(id);
    return ok(sessions.value(id));
}


QJsonObject MainWindow::downloadLocalFile(const QJsonObject &input) {
    const QString sessionId = input.value("session_id").toString();
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
    const QJsonObject session = sessions.value(sessionId);
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved") return fail("PERMISSION_DENIED", "设备所有者尚未批准此会话");
    bool canRead = false;
    for (const QJsonValue &permission : session.value("permissions").toArray()) {
        if (permission.toString() == QLatin1String("read_file")) canRead = true;
    }
    if (!canRead) return fail("PERMISSION_DENIED", "会话未获得 read_file 权限");
    const QString remotePath = input.value("remote_path").toString().trimmed();
    if (remotePath.isEmpty() || remotePath.size() > 4096) return fail("INVALID_ARGUMENT", "远程路径为空或过长");
    QFileInfo remoteInfo(remotePath);
    if (!remoteInfo.exists() || !remoteInfo.isFile()) return fail("FILE_NOT_FOUND", "远程路径不是可读取的普通文件");
    constexpr qint64 maxDownloadBytes = 32LL * 1024 * 1024;
    if (remoteInfo.size() > maxDownloadBytes) return fail("FILE_TOO_LARGE", "文件超过 32 MiB 下载上限");
    QString destination = input.value("destination_path").toString().trimmed();
    if (!destination.isEmpty()) {
        if (!QFileInfo(destination).isAbsolute()) return fail("INVALID_ARGUMENT", "destination_path 必须是绝对路径");
        if (QFileInfo::exists(destination)) return fail("LOCAL_PATH_EXISTS", "destination_path 已存在，拒绝覆盖");
    } else {
        const QString root = QDir(QStandardPaths::writableLocation(QStandardPaths::TempLocation))
            .filePath(QString("yiuk-downloads/%1-%2").arg(QDateTime::currentMSecsSinceEpoch()).arg(remoteInfo.fileName()));
        destination = root;
    }
    if (!QDir().mkpath(QFileInfo(destination).absolutePath())) return fail("LOCAL_WRITE_FAILED", "无法创建下载目录");
    QFile source(remotePath);
    if (!source.open(QIODevice::ReadOnly)) return fail("FILE_NOT_FOUND", "无法打开远程文件");
    QSaveFile output(destination);
    if (!output.open(QIODevice::WriteOnly)) return fail("LOCAL_WRITE_FAILED", "无法创建本地文件");
    QCryptographicHash hash(QCryptographicHash::Sha256);
    qint64 total = 0;
    while (!source.atEnd()) {
        const QByteArray chunk = source.read(64 * 1024);
        if (chunk.isEmpty() && source.error() != QFile::NoError) {
            output.cancelWriting();
            return fail("FILE_READ_FAILED", "读取远程文件失败");
        }
        if (output.write(chunk) != chunk.size()) {
            output.cancelWriting();
            return fail("LOCAL_WRITE_FAILED", "写入本地文件失败");
        }
        hash.addData(chunk);
        total += chunk.size();
    }
    if (remoteInfo.size() >= 0 && total != remoteInfo.size()) {
        output.cancelWriting();
        return fail("FILE_INTEGRITY_FAILED", "读取文件大小与预期不一致");
    }
    if (!output.commit()) return fail("LOCAL_WRITE_FAILED", "提交本地文件失败");
    return ok(QJsonObject{{"session_id", sessionId}, {"remote_path", remotePath},
                          {"file_name", remoteInfo.fileName()}, {"local_path", destination},
                          {"size", static_cast<double>(total)},
                          {"sha256", QString::fromLatin1(hash.result().toHex())},
                          {"status", "succeeded"}});
}


// uploadLocalFile 将本地文件复制到本机的 uploads 目录
// 本地桥接没有远程设备，因此源文件和目标文件位于同一台主机
QJsonObject MainWindow::uploadLocalFile(const QJsonObject &input) {
    const QString sessionId = input.value("session_id").toString();
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
    const QJsonObject session = sessions.value(sessionId);
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved") return fail("PERMISSION_DENIED", "设备所有者尚未批准此会话");
    bool canWrite = false;
    for (const QJsonValue &permission : session.value("permissions").toArray()) {
        if (permission.toString() == QLatin1String("write_file")) canWrite = true;
    }
    if (!canWrite) return fail("PERMISSION_DENIED", "会话未获得 write_file 权限");
    const QString localPath = input.value("local_path").toString().trimmed();
    if (localPath.isEmpty() || !QFileInfo(localPath).isAbsolute()) {
        return fail("INVALID_ARGUMENT", "local_path 必须是绝对路径");
    }
    QFileInfo localInfo(localPath);
    if (!localInfo.exists() || !localInfo.isFile()) return fail("LOCAL_READ_FAILED", "local_path 不是可读取的普通文件");
    constexpr qint64 maxUploadBytes = 32LL * 1024 * 1024;
    if (localInfo.size() > maxUploadBytes) return fail("FILE_TOO_LARGE", "文件超过 32 MiB 上传上限");
    const QString requested = input.value("remote_path").toString().trimmed();
    QString target;
    if (requested.isEmpty()) {
        target = QDir(yiukConfigRoot())
            .filePath(QString("uploads/%1-%2").arg(QDateTime::currentMSecsSinceEpoch()).arg(localInfo.fileName()));
    } else {
        if (!QFileInfo(requested).isAbsolute()) return fail("INVALID_ARGUMENT", "remote_path 必须是绝对路径");
        QFileInfo requestedInfo(requested);
        target = requestedInfo.isDir() ? QDir(requested).filePath(localInfo.fileName()) : requested;
    }
    const bool overwrite = input.value("overwrite").toBool(false);
    if (QFileInfo::exists(target) && !overwrite) return fail("REMOTE_PATH_EXISTS", "目标文件已存在；如需覆盖请设置 overwrite=true");
    if (!QDir().mkpath(QFileInfo(target).absolutePath())) return fail("LOCAL_WRITE_FAILED", "无法创建目标目录");
    QFile source(localPath);
    if (!source.open(QIODevice::ReadOnly)) return fail("LOCAL_READ_FAILED", "无法打开 local_path");
    QSaveFile output(target);
    if (!output.open(QIODevice::WriteOnly)) return fail("LOCAL_WRITE_FAILED", "无法创建目标文件");
    QCryptographicHash hash(QCryptographicHash::Sha256);
    qint64 total = 0;
    while (!source.atEnd()) {
        const QByteArray chunk = source.read(64 * 1024);
        if (chunk.isEmpty() && source.error() != QFile::NoError) {
            output.cancelWriting();
            return fail("LOCAL_READ_FAILED", "读取 local_path 失败");
        }
        if (output.write(chunk) != chunk.size()) {
            output.cancelWriting();
            return fail("LOCAL_WRITE_FAILED", "写入目标文件失败");
        }
        hash.addData(chunk);
        total += chunk.size();
    }
    if (total != localInfo.size()) {
        output.cancelWriting();
        return fail("FILE_INTEGRITY_FAILED", "读取文件大小与预期不一致");
    }
    if (!output.commit()) return fail("LOCAL_WRITE_FAILED", "提交目标文件失败");
    return ok(QJsonObject{{"session_id", sessionId}, {"local_path", localPath},
                          {"remote_path", target}, {"file_name", QFileInfo(target).fileName()},
                          {"size", static_cast<double>(total)},
                          {"sha256", QString::fromLatin1(hash.result().toHex())},
                          {"overwrite", overwrite}, {"status", "succeeded"}});
}


QJsonObject MainWindow::executeLocalCommand(const QJsonObject &input) {
    const QString sessionId = input.value("session_id").toString();
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
    if (sessions.value(sessionId).value("status").toString() != "active") return fail("PERMISSION_DENIED", "设备所有者尚未批准此会话");
    const QString command = input.value("command").toString();
    if (command.trimmed().isEmpty() || command.size() > 32768) return fail("INVALID_ARGUMENT", "命令为空或超过 32 KiB");
    const QString requestId = QUuid::createUuid().toString(QUuid::WithoutBraces);
    const int timeout = qBound(1, input.value("timeout_seconds").toInt(60), 3600);
    const QString requestedWorkingDirectory = input.value("working_directory").toString().trimmed();
    const QString cwd = requestedWorkingDirectory.isEmpty()
        ? defaultCommandWorkingDirectory() : requestedWorkingDirectory;

    tasks.insert(requestId, {{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"},
                             {"stdout", ""}, {"stderr", ""}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}});

    PowerShellRunner::Hooks hooks;
    hooks.onOutput = [this, requestId](const QByteArray &accepted, bool standardError, bool truncated) {
        QJsonObject task = tasks.value(requestId);
        const QString key = standardError ? QStringLiteral("stderr") : QStringLiteral("stdout");
        task.insert(key, task.value(key).toString() + QString::fromUtf8(accepted));
        if (truncated) task.insert("truncated", true);
        tasks.insert(requestId, task);
    };
    hooks.onFinished = [this, requestId](const QString &status, int exitCode, const QString &error) {
        if (PowerShellRunner *runner = runs.take(requestId)) runner->deleteLater();
        QJsonObject task = tasks.value(requestId);
        if (status == QLatin1String("timed_out")) {
            task.insert("status", "timed_out");
            tasks.insert(requestId, task);
        } else if (status != QLatin1String("cancelled")) {
            if (!error.isEmpty()) {
                task.insert("status", "failed");
                task.insert("stderr", task.value("stderr").toString() + error);
            } else {
                task.insert("status", status);
                task.insert("exit_code", exitCode);
            }
            tasks.insert(requestId, task);
        }
        persistSessionState();
        refreshSessionsPage();
        refreshActiveSessions();
        if (status == QLatin1String("timed_out"))
            showStatusMessage("PowerShell 任务已超时", 4000);
        else if (status != QLatin1String("cancelled"))
            showStatusMessage("PowerShell 任务完成", 3000);
    };

    auto *runner = new PowerShellRunner(this, requestId, command, cwd, timeout, hooks);
    runs.insert(requestId, runner);
    QString startError;
    if (!runner->start(&startError)) {
        runs.remove(requestId);
        runner->deleteLater();
        tasks.remove(requestId);
        return fail("EXECUTION_FAILED", startError);
    }
    showStatusMessage("正在执行本机 PowerShell 任务", 3000);
    refreshActiveSessions();
    return ok(QJsonObject{{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"}});
}


QJsonObject MainWindow::commandResult(const QJsonObject &input) const {
    const QString requestId = input.value("request_id").toString();
    if (!tasks.contains(requestId) || tasks.value(requestId).value("session_id").toString() != input.value("session_id").toString())
        return fail("TASK_NOT_FOUND", "任务不存在或不属于该会话");
    QJsonObject result = tasks.value(requestId);
    const int cursor = qMax(0, input.value("cursor").toInt());
    result.insert("stdout", result.value("stdout").toString().mid(cursor));
    return ok(result);
}


QJsonObject MainWindow::cancelCommand(const QJsonObject &input) {
    const QString requestId = input.value("request_id").toString();
    if (!tasks.contains(requestId) || tasks.value(requestId).value("session_id").toString() != input.value("session_id").toString())
        return fail("TASK_NOT_FOUND", "任务不存在或不属于该会话");
    QJsonObject task = tasks.value(requestId);
    task.insert("status", "cancelled");
    tasks.insert(requestId, task);
    if (PowerShellRunner *runner = runs.value(requestId)) runner->cancel();
    persistSessionState();
    refreshSessionsPage();
    return ok(QJsonObject{{"request_id", requestId}, {"status", "cancelled"}});
}
