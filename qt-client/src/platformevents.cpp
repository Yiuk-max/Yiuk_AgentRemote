#include "mainwindow.h"

#include "input/inputcontroller.h"
#include "nativeops.h"
#include "vision/screengrab.h"
#include "vision/visionservice.h"

#include "powershellrunner.h"

#include <QDateTime>
#include <QThread>


// storeIncomingDeviceSession 记录本客户端作为被协助设备参与的会话
// 包括平台跳过批准请求的直连模式
void MainWindow::storeIncomingDeviceSession(const QJsonObject &session, bool approved) {
    const QString sessionId = session.value("session_id").toString();
    if (sessionId.isEmpty()) return;
    if (!canAdmitPlatformSession(session)) {
        if (platform && platform->isConnected()) platform->requestSessionClose(sessionId);
        return;
    }
    const bool isNewSession = !sessions.contains(sessionId);
    QJsonObject sessionRecord = session;
    sessionRecord.insert("source", "platform");
    if (!sessionRecord.contains("expires_at_ms") && sessionRecord.value("expires_at").isDouble())
        sessionRecord.insert("expires_at_ms", sessionRecord.value("expires_at").toDouble() * 1000.0);
    if (sessionRecord.value("requester").toString().isEmpty())
        sessionRecord.insert("requester", session.value("operator").toString("平台操作者"));
    if (sessionRecord.value("source_device").toString().isEmpty())
        sessionRecord.insert("source_device", session.value("operator_device").toString());
    sessions.insert(sessionId, sessionRecord);
    if (isNewSession) {
        appendActivity(sessionId, approved
            ? QString("Agent 已直接连接（无需批准）：%1").arg(sessionRecord.value("purpose").toString())
            : QString("收到 Agent 协助请求：%1").arg(sessionRecord.value("purpose").toString()),
            ActivityKind::Session);
        notificationSessions.insert(sessionId, sessionRecord.value("purpose").toString());
    }
    persistSessionState();
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
    showStatusMessage(approved ? "已建立直接连接会话" : "有待批准的协助请求", 4000);
    if (approved) openOrActivateActivityPanel(sessionId);
    if (isNewSession && trayIcon && QSystemTrayIcon::supportsMessages()) {
        trayIcon->showMessage(approved ? "远程协助已连接" : "需要批准远程协助",
                              sessionRecord.value("purpose").toString(),
                              QSystemTrayIcon::Information, 10000);
    }
}


void MainWindow::onPlatformSessionRequested(const QJsonObject &session) {
    const QString sessionId = session.value("session_id").toString();
    if (sessionId.isEmpty() || sessions.contains(sessionId)) return;
    if (!canAdmitPlatformSession(session)) {
        if (platform && platform->isConnected()) platform->rejectSession(sessionId);
        QJsonObject rejected = session;
        rejected.insert("source", "platform");
        rejected.insert("status", "rejected");
        rejected.insert("end_reason", "已有活动会话");
        sessions.insert(sessionId, rejected);
        persistSessionState();
        refreshSessionsPage();
        showStatusMessage("已有一个远程会话，已拒绝新的连接请求", 5000);
        return;
    }
    storeIncomingDeviceSession(session, false);
    if (trayIcon && QSystemTrayIcon::supportsMessages()) return;
    QTimer::singleShot(0, this, [this, sessionId] { showApprovalDialog(sessionId); });
}


void MainWindow::onPlatformSessionUpdated(const QJsonObject &session) {
    const QString sessionId = session.value("session_id").toString();
    if (sessionId.isEmpty()) return;
    const QJsonObject existing = sessions.value(sessionId);
    const bool hasStoredSessionSource = existing.contains("source");
    if (!canAdmitPlatformSession(session)) {
        if (platform && platform->isConnected()) platform->requestSessionClose(sessionId);
        QJsonObject rejected = session;
        rejected.insert("source", "platform");
        rejected.insert("status", "closed");
        rejected.insert("end_reason", "已有活动会话");
        sessions.insert(sessionId, rejected);
        persistSessionState();
        refreshSessionsPage();
        return;
    }
    QJsonObject sessionRecord = session;
    if (!sessionRecord.contains("source")) {
        if (hasStoredSessionSource) {
            sessionRecord.insert("source", existing.value("source"));
        } else if (session.value("device_id").toString() == deviceId()) {
            // direct 模式：平台直接下发已批准会话，没有 session_request 阶段
            storeIncomingDeviceSession(session, session.value("status").toString() == "approved");
            return;
        } else {
            sessionRecord.insert("source", "platform-operator");
        }
    }
    if (!sessionRecord.contains("expires_at_ms") && sessionRecord.value("expires_at").isDouble())
        sessionRecord.insert("expires_at_ms", sessionRecord.value("expires_at").toDouble() * 1000.0);
    if (sessionRecord.value("source").toString() == QLatin1String("platform")) {
        if (sessionRecord.value("requester").toString().isEmpty())
            sessionRecord.insert("requester", session.value("operator").toString("平台操作者"));
        if (sessionRecord.value("source_device").toString().isEmpty())
            sessionRecord.insert("source_device", session.value("operator_device").toString());
    }
    const bool statusChanged = existing.value("status").toString() != sessionRecord.value("status").toString();
    sessions.insert(sessionId, sessionRecord);
    if (statusChanged)
        appendActivity(sessionId, QString("平台会话状态：%1").arg(session.value("status").toString()),
                       ActivityKind::Session);
    persistSessionState();
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved" && status != "pending_approval") {
        cancelSessionCommands(sessionId, status == "closed" ? "cancelled" : "result_unknown");
        if (nativeOps) nativeOps->cancelSession(sessionId);
        // 会话结束时删掉本会话的临时截图，并让旧 screenshot_id 失效。
        if (vision) vision->forgetSession(sessionId);
        discardSessionUploads(sessionId);
    }
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
}


void MainWindow::onPlatformSessionCreated(const QJsonObject &session) {
    if (session.value("device_id").toString() == deviceId()) {
        if (!canAdmitPlatformSession(session)) {
            const QString sessionId = session.value("session_id").toString();
            if (platform && platform->isConnected()) platform->requestSessionClose(sessionId);
            return;
        }
        storeIncomingDeviceSession(session, session.value("status").toString() == "approved");
        return;
    }
    const QString sessionId = session.value("session_id").toString();
    if (sessionId.isEmpty()) return;
    QJsonObject sessionRecord = session;
    sessionRecord.insert("source", "platform-operator");
    if (!sessionRecord.contains("expires_at_ms") && sessionRecord.value("expires_at").isDouble())
        sessionRecord.insert("expires_at_ms", sessionRecord.value("expires_at").toDouble() * 1000.0);
    sessions.insert(sessionId, sessionRecord);
    persistSessionState();
    refreshSessionsPage();
    const QString status = session.value("status").toString();
    showStatusMessage(status == "approved" ? "会话已批准" : "已发送协助请求，等待对方批准", 4000);
}


void MainWindow::onPlatformCloseRequested(const QString &sessionId) {
    appendActivity(sessionId, "平台已请求结束会话", ActivityKind::Session);
    cancelSessionCommands(sessionId, "cancelled");
    // 原生操作也要停：会话没了，它们的截图与输入都不该继续。
    if (nativeOps) nativeOps->cancelSession(sessionId);
    if (vision) vision->forgetSession(sessionId);
    QJsonObject session = sessions.value(sessionId);
    session.insert("status", "closed");
    sessions.insert(sessionId, session);
    persistSessionState();
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
}


void MainWindow::onPlatformCancelRequested(const QString &sessionId, const QString &requestId) {
    appendActivity(sessionId, QString("Agent 请求停止任务：%1").arg(requestId.left(8)));
    // 原生操作与命令共用 cancel 语义：先置位取消标记，处理器自己决定要多久退出。
    if (nativeOps) nativeOps->cancel(requestId);
    QJsonObject commandTask = tasks.value(requestId);
    commandTask.insert("status", "cancelled");
    commandTask.insert("exit_code", QJsonValue(QJsonValue::Null));
    tasks.insert(requestId, commandTask);
    persistSessionState();
    if (PowerShellRunner *runner = commandRunners.value(requestId)) runner->cancel();
    nextPlatformOutputSequenceByRequest.remove(requestId);
    platformSessionByRequest.remove(requestId);
    if (platform && platform->isConnected())
        platform->sendCommandResult(sessionId, requestId, "cancelled", QJsonValue(), true, false);
    refreshSessionsPage();
    refreshActiveSessions();
}


bool MainWindow::sessionAllowsFileRead(const QString &sessionId) {
    expireSessionIfNeeded(sessionId);
    if (!sessions.contains(sessionId)) return false;
    const QJsonObject session = sessions.value(sessionId);
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved") return false;
    const QJsonArray permissions = session.value("permissions").toArray();
    for (const QJsonValue &permission : permissions) {
        if (permission.toString() == QLatin1String("read_file")) return true;
    }
    return false;
}


// 上报下载结果并释放文件和校验资源。
void MainWindow::reportDownloadResultAndCleanup(const QSharedPointer<FileDownloadState> &transferState,
                                               const QString &status, bool resultKnown) {
    if (!transferState || transferState->requestId.isEmpty()) return;
    const QString fileSHA256 = status == QLatin1String("succeeded") && transferState->checksum
        ? QString::fromLatin1(transferState->checksum->result().toHex()) : QString();
    if (platform && platform->isConnected()) {
        platform->sendFileDownloadResult(transferState->sessionId, transferState->requestId, status, transferState->fileName,
                                         transferState->sentBytes, fileSHA256, resultKnown);
    }
    activeFileDownloads.remove(transferState->requestId);
    if (transferState->file) {
        transferState->file->close();
        transferState->file->deleteLater();
        transferState->file = nullptr;
    }
    delete transferState->checksum;
    transferState->checksum = nullptr;
    refreshActiveSessions();
}


// 每次最多发送一个分块，再通过事件循环调度下一块；结束或失去授权时收敛传输状态。
void MainWindow::sendNextDownloadChunk(const QSharedPointer<FileDownloadState> &transferState) {
    if (!transferState) return;
    if (!activeFileDownloads.contains(transferState->requestId)) {
        if (transferState->file) {
            transferState->file->close();
            transferState->file->deleteLater();
        }
        delete transferState->checksum;
        return;
    }
    if (!sessionAllowsFileRead(transferState->sessionId)) {
        appendActivity(transferState->sessionId, QString("文件下载 %1 已停止：会话不再授权读取").arg(transferState->requestId.left(8)),
                       ActivityKind::Error);
        reportDownloadResultAndCleanup(transferState, QStringLiteral("cancelled"), true);
        return;
    }
    if (transferState->file->atEnd()) {
        reportDownloadResultAndCleanup(transferState, QStringLiteral("succeeded"), true);
        return;
    }
    constexpr qint64 chunkSize = 64 * 1024;
    const QByteArray chunk = transferState->file->read(chunkSize);
    if (chunk.isEmpty() && transferState->file->error() != QFile::NoError) {
        appendActivity(transferState->sessionId, QString("文件下载 %1 读取失败：%2").arg(transferState->requestId.left(8), transferState->file->errorString()),
                       ActivityKind::Error);
        reportDownloadResultAndCleanup(transferState, QStringLiteral("failed"), true);
        return;
    }
    constexpr qint64 maxDownloadBytes = 32LL * 1024 * 1024;
    if (transferState->sentBytes + chunk.size() > maxDownloadBytes) {
        appendActivity(transferState->sessionId, QString("文件下载 %1 已停止：文件超过 32 MiB 上限").arg(transferState->requestId.left(8)),
                       ActivityKind::Error);
        reportDownloadResultAndCleanup(transferState, QStringLiteral("failed"), true);
        return;
    }
    transferState->checksum->addData(chunk);
    transferState->sentBytes += chunk.size();
    const bool eof = transferState->file->atEnd();
    if (platform && platform->isConnected()) {
        platform->sendFileDownloadChunk(transferState->sessionId, transferState->requestId, transferState->nextSequence++, chunk, eof);
    }
    if (eof) {
        if (transferState->expectedBytes >= 0 && transferState->sentBytes != transferState->expectedBytes) {
            appendActivity(transferState->sessionId, QString("文件下载 %1 失败：读取大小与预期不一致").arg(transferState->requestId.left(8)),
                           ActivityKind::Error);
            reportDownloadResultAndCleanup(transferState, QStringLiteral("failed"), true);
            return;
        }
        appendActivity(transferState->sessionId, QString("文件下载 %1 完成：%2 字节").arg(transferState->requestId.left(8)).arg(transferState->sentBytes),
                       ActivityKind::Success);
        reportDownloadResultAndCleanup(transferState, QStringLiteral("succeeded"), true);
        return;
    }
    QTimer::singleShot(0, this, [this, transferState] { sendNextDownloadChunk(transferState); });
}


void MainWindow::onPlatformFileDownloadRequested(const QString &sessionId, const QString &requestId,
                                     const QString &remotePath) {
    auto reportTransferFailure = [this, sessionId, requestId](const QString &message) {
        appendActivity(sessionId, QString("文件下载 %1 被拒绝：%2").arg(requestId.left(8), message),
                       ActivityKind::Error);
        if (platform && platform->isConnected())
            platform->sendFileDownloadResult(sessionId, requestId, QStringLiteral("failed"), QString(), 0, QString(), true);
    };
    if (requestId.isEmpty() || activeFileDownloads.contains(requestId)) return;
    if (!sessionAllowsFileRead(sessionId)) {
        reportTransferFailure(QStringLiteral("会话未获得 read_file 权限或已结束"));
        return;
    }
    const QString sourcePath = remotePath.trimmed();
    if (sourcePath.isEmpty() || sourcePath.size() > 4096) {
        reportTransferFailure(QStringLiteral("远程路径为空或过长"));
        return;
    }
    QFileInfo sourceFileInfo(sourcePath);
    if (!sourceFileInfo.exists() || !sourceFileInfo.isFile()) {
        reportTransferFailure(QStringLiteral("远程路径不是可读取的普通文件"));
        return;
    }
    constexpr qint64 maxDownloadBytes = 32LL * 1024 * 1024;
    if (sourceFileInfo.size() > maxDownloadBytes) {
        reportTransferFailure(QStringLiteral("文件超过 32 MiB 下载上限"));
        return;
    }
    auto *file = new QFile(sourcePath, this);
    if (!file->open(QIODevice::ReadOnly)) {
        const QString error = file->errorString();
        file->deleteLater();
        reportTransferFailure(QStringLiteral("无法打开文件：%1").arg(error));
        return;
    }
    auto transferState = QSharedPointer<FileDownloadState>::create();
    transferState->sessionId = sessionId;
    transferState->requestId = requestId;
    transferState->fileName = sourceFileInfo.fileName();
    transferState->file = file;
    transferState->checksum = new QCryptographicHash(QCryptographicHash::Sha256);
    transferState->expectedBytes = sourceFileInfo.size();
    activeFileDownloads.insert(requestId, true);
    appendActivity(sessionId, QString("Agent 请求下载文件 %1（%2 字节）").arg(transferState->fileName).arg(sourceFileInfo.size()),
                   ActivityKind::Download);
    refreshActiveSessions();
    QTimer::singleShot(0, this, [this, transferState] { sendNextDownloadChunk(transferState); });
}


bool MainWindow::sessionAllowsFileWrite(const QString &sessionId) {
    const QJsonObject session = sessions.value(sessionId);
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved") return false;
    const QJsonArray permissions = session.value("permissions").toArray();
    for (const QJsonValue &permission : permissions) {
        if (permission.toString() == QLatin1String("write_file")) return true;
    }
    return false;
}


namespace {

constexpr qint64 kMaxUploadBytes = 32LL * 1024 * 1024;

// 确保操作端提供的文件名可作为单独的路径组成部分使用
QString sanitizeUploadFileName(const QString &fileName) {
    QString baseName = fileName;
    baseName.replace(QLatin1Char('\\'), QLatin1Char('/'));
    const int lastPathSeparator = baseName.lastIndexOf(QLatin1Char('/'));
    if (lastPathSeparator >= 0) baseName = baseName.mid(lastPathSeparator + 1);
    QString sanitizedName;
    sanitizedName.reserve(baseName.size());
    for (const QChar character : baseName) {
        if (character.unicode() < 0x20 || QStringLiteral("<>:\"/|?*").contains(character)) sanitizedName += QLatin1Char('_');
        else sanitizedName += character;
    }
    sanitizedName = sanitizedName.trimmed();
    while (sanitizedName.startsWith(QLatin1Char('.'))) sanitizedName.remove(0, 1);
    if (sanitizedName.isEmpty()) sanitizedName = QStringLiteral("upload.bin");
    return sanitizedName.left(160);
}

}  // namespace


// reportUploadResultAndCleanup 在会话仍有效时上报最终结果，并释放文件和校验资源。
void MainWindow::reportUploadResultAndCleanup(const QSharedPointer<FileUploadState> &transferState, const QString &status) {
    if (!transferState || transferState->requestId.isEmpty()) return;
    const QString sessionStatusValue = sessions.value(transferState->sessionId).value("status").toString();
    const bool sessionLive = sessionStatusValue == "active" || sessionStatusValue == "approved";
    QString fileSHA256;
    if (status == QLatin1String("succeeded") && transferState->checksum)
        fileSHA256 = QString::fromLatin1(transferState->checksum->result().toHex());
    if (transferState->file) {
        transferState->file->close();
        transferState->file->deleteLater();
        transferState->file = nullptr;
    }
    delete transferState->checksum;
    transferState->checksum = nullptr;
    activeFileUploads.remove(transferState->requestId);
    if (status != QLatin1String("succeeded") && !transferState->temporaryPath.isEmpty())
        QFile::remove(transferState->temporaryPath);
    if (sessionLive && platform && platform->isConnected()) {
        platform->sendFileUploadResult(transferState->sessionId, transferState->requestId, status,
                                      status == QLatin1String("succeeded") ? transferState->targetPath : QString(),
                                      transferState->fileName,
                                      status == QLatin1String("succeeded") ? transferState->receivedBytes : 0,
                                      fileSHA256, true);
    }
    refreshActiveSessions();
}


// discardSessionUploads 终止会话的上传并删除临时文件，不再向平台上报结果。
// 会话已由平台结束或因断线失效。
void MainWindow::discardSessionUploads(const QString &sessionId) {
    const QStringList requestIds = activeFileUploads.keys();
    for (const QString &requestId : requestIds) {
        QSharedPointer<FileUploadState> transferState = activeFileUploads.value(requestId);
        if (!transferState || transferState->sessionId != sessionId) continue;
        appendActivity(sessionId, QString("文件上传 %1 已停止：会话已结束").arg(requestId.left(8)),
                       ActivityKind::Error);
        if (transferState->file) {
            transferState->file->close();
            transferState->file->deleteLater();
            transferState->file = nullptr;
        }
        delete transferState->checksum;
        transferState->checksum = nullptr;
        if (!transferState->temporaryPath.isEmpty()) QFile::remove(transferState->temporaryPath);
        activeFileUploads.remove(requestId);
    }
    refreshActiveSessions();
}


void MainWindow::onPlatformFileUploadRequested(const QString &sessionId, const QString &requestId,
                                               const QString &remotePath, const QString &fileName,
                                               qint64 fileSize, const QString &sha256, bool overwrite) {
    auto reportTransferFailure = [this, sessionId, requestId, fileName](const QString &message) {
        appendActivity(sessionId, QString("文件上传 %1 被拒绝：%2").arg(requestId.left(8), message),
                       ActivityKind::Error);
        if (platform && platform->isConnected())
            platform->sendFileUploadResult(sessionId, requestId, QStringLiteral("failed"), QString(),
                                           fileName, 0, QString(), true);
    };
    if (requestId.isEmpty() || activeFileUploads.contains(requestId)) return;
    if (!sessionAllowsFileWrite(sessionId)) {
        reportTransferFailure(QStringLiteral("会话未获得 write_file 权限或已结束"));
        return;
    }
    if (fileSize < 0 || fileSize > kMaxUploadBytes) {
        reportTransferFailure(QStringLiteral("文件超过 32 MiB 上传上限"));
        return;
    }
    if (sha256.size() > 128) {
        reportTransferFailure(QStringLiteral("校验值过长"));
        return;
    }

    const QString sanitizedFileName = sanitizeUploadFileName(fileName);
    const QString requestedPath = remotePath.trimmed();
    QString targetPath;
    if (requestedPath.isEmpty()) {
        const QString dir = QDir(yiukConfigRoot()).filePath("uploads");
        targetPath = QDir(dir).filePath(QString("%1-%2").arg(QDateTime::currentMSecsSinceEpoch()).arg(sanitizedFileName));
    } else {
        if (!QDir::isAbsolutePath(requestedPath)) {
            reportTransferFailure(QStringLiteral("远程路径必须是绝对路径"));
            return;
        }
        const QFileInfo requestedInfo(requestedPath);
        targetPath = requestedInfo.isDir() ? QDir(requestedPath).filePath(sanitizedFileName) : requestedPath;
    }
    if (targetPath.isEmpty() || targetPath.size() > 4096) {
        reportTransferFailure(QStringLiteral("目标路径无效或过长"));
        return;
    }
    const QFileInfo targetInfo(targetPath);
    if (targetInfo.exists() && !overwrite) {
        reportTransferFailure(QStringLiteral("目标文件已存在；如需覆盖请设置 overwrite=true"));
        return;
    }
    if (!QDir().mkpath(targetInfo.absolutePath())) {
        reportTransferFailure(QStringLiteral("无法创建目标目录"));
        return;
    }
    const QString tempPath = targetPath + QString(".yiuk-upload-%1.part").arg(requestId.left(8));
    QFile::remove(tempPath);
    auto *file = new QFile(tempPath, this);
    if (!file->open(QIODevice::WriteOnly | QIODevice::Truncate)) {
        const QString error = file->errorString();
        file->deleteLater();
        reportTransferFailure(QStringLiteral("无法创建目标文件：%1").arg(error));
        return;
    }
    auto transferState = QSharedPointer<FileUploadState>::create();
    transferState->sessionId = sessionId;
    transferState->requestId = requestId;
    transferState->targetPath = targetPath;
    transferState->temporaryPath = tempPath;
    transferState->fileName = sanitizedFileName;
    transferState->file = file;
    transferState->checksum = new QCryptographicHash(QCryptographicHash::Sha256);
    transferState->expectedBytes = fileSize;
    transferState->expectedSha256 = sha256.toLower();
    transferState->overwrite = overwrite;
    activeFileUploads.insert(requestId, transferState);
    appendActivity(sessionId, QString("Agent 开始上传文件 %1（%2 字节）→ %3").arg(sanitizedFileName).arg(fileSize).arg(targetPath),
                   ActivityKind::Upload);
    refreshActiveSessions();
    if (fileSize == 0) {
        // 空文件不包含数据分块；文件信息由请求元数据携带
        QFile::remove(targetPath);
        if (!QFile::rename(tempPath, targetPath)) {
            appendActivity(sessionId, QString("文件上传 %1 失败：无法创建空文件").arg(requestId.left(8)),
                           ActivityKind::Error);
            reportUploadResultAndCleanup(transferState, QStringLiteral("failed"));
            return;
        }
        appendActivity(sessionId, QString("文件上传 %1 完成：0 字节").arg(requestId.left(8)),
                       ActivityKind::Success);
        reportUploadResultAndCleanup(transferState, QStringLiteral("succeeded"));
    }
}


void MainWindow::onPlatformFileUploadChunk(const QString &sessionId, const QString &requestId,
                                           quint64 sequence, const QString &base64Data, bool eof) {
    auto transferState = activeFileUploads.value(requestId);
    if (!transferState || transferState->sessionId != sessionId || !transferState->file) return;
    auto reportTransferFailure = [this, transferState, requestId](const QString &message) {
        appendActivity(transferState->sessionId, QString("文件上传 %1 失败：%2").arg(requestId.left(8), message),
                       ActivityKind::Error);
        reportUploadResultAndCleanup(transferState, QStringLiteral("failed"));
    };
    if (!sessionAllowsFileWrite(sessionId)) {
        appendActivity(sessionId, QString("文件上传 %1 已停止：会话不再授权写入").arg(requestId.left(8)),
                       ActivityKind::Error);
        reportUploadResultAndCleanup(transferState, QStringLiteral("cancelled"));
        return;
    }
    if (sequence != transferState->expectedSequence) {
        reportTransferFailure(QStringLiteral("分块序号不连续"));
        return;
    }
    const QByteArray chunk = QByteArray::fromBase64(base64Data.toLatin1());
    if (chunk.isEmpty() && !base64Data.isEmpty()) {
        reportTransferFailure(QStringLiteral("分块数据不是有效的 Base64"));
        return;
    }
    if (transferState->receivedBytes + chunk.size() > kMaxUploadBytes ||
        (transferState->expectedBytes > 0 && transferState->receivedBytes + chunk.size() > transferState->expectedBytes)) {
        reportTransferFailure(QStringLiteral("写入字节数超过声明的文件大小"));
        return;
    }
    if (!chunk.isEmpty() && transferState->file->write(chunk) != chunk.size()) {
        reportTransferFailure(QStringLiteral("写入目标文件失败：%1").arg(transferState->file->errorString()));
        return;
    }
    transferState->checksum->addData(chunk);
    transferState->receivedBytes += chunk.size();
    transferState->expectedSequence++;
    if (!eof) return;

    transferState->file->flush();
    transferState->file->close();
    transferState->file->deleteLater();
    transferState->file = nullptr;
    if (transferState->receivedBytes != transferState->expectedBytes) {
        appendActivity(sessionId, QString("文件上传 %1 失败：收到 %2 字节，声明 %3 字节")
                                   .arg(requestId.left(8)).arg(transferState->receivedBytes).arg(transferState->expectedBytes),
                       ActivityKind::Error);
        reportUploadResultAndCleanup(transferState, QStringLiteral("failed"));
        return;
    }
    if (!transferState->expectedSha256.isEmpty()) {
        const QString actualSHA256 = QString::fromLatin1(transferState->checksum->result().toHex());
        if (actualSHA256 != transferState->expectedSha256) {
            appendActivity(sessionId, QString("文件上传 %1 失败：校验值不一致").arg(requestId.left(8)),
                           ActivityKind::Error);
            reportUploadResultAndCleanup(transferState, QStringLiteral("failed"));
            return;
        }
    }
    if (transferState->overwrite) QFile::remove(transferState->targetPath);
    if (!QFile::rename(transferState->temporaryPath, transferState->targetPath)) {
        appendActivity(sessionId, QString("文件上传 %1 失败：无法写入目标文件 %2")
                                   .arg(requestId.left(8), transferState->targetPath),
                       ActivityKind::Error);
        reportUploadResultAndCleanup(transferState, QStringLiteral("failed"));
        return;
    }
    appendActivity(sessionId, QString("文件上传 %1 完成：%2 字节 → %3")
                               .arg(requestId.left(8)).arg(transferState->receivedBytes).arg(transferState->targetPath),
                   ActivityKind::Success);
    reportUploadResultAndCleanup(transferState, QStringLiteral("succeeded"));
}


namespace {

// 可取消的等待：点击后的 settle 与移动后的 settle 都不能吞掉取消，
// 否则"取消一个已经点完的请求"要等满 settle 才会退出。
bool waitCancellable(int milliseconds, const std::atomic_bool *cancelled) {
    int remaining = milliseconds;
    while (remaining > 0) {
        if (cancelled != nullptr && cancelled->load()) return false;
        const int slice = qMin(remaining, 10);
        QThread::msleep(static_cast<unsigned long>(slice));
        remaining -= slice;
    }
    return cancelled == nullptr || !cancelled->load();
}

// 视觉/输入类错误的下一步建议：不要只说"失败了"。
QString nativeHintForCode(const QString &code) {
    if (code == QLatin1String("STALE_SCREENSHOT"))
        return QStringLiteral("重新 take_screenshot，并用新截图的坐标重试");
    if (code == QLatin1String("SCREENSHOT_REQUIRED"))
        return QStringLiteral("先调用 take_screenshot 建立坐标基准");
    if (code == QLatin1String("INPUT_FAILED") || code == QLatin1String("SCREENSHOT_FAILED"))
        return QStringLiteral("确认设备未锁屏、没有停留在 UAC 安全桌面");
    return QString();
}

}  // namespace


void MainWindow::setupNativeOperations() {
    if (nativeOps) return;
    nativeOps = new NativeOperationDispatcher(this);
    // 所有抓屏/视觉重操作都走同一个 VisionService：会话级互斥、500ms 最小间隔、
    // 统一 screenshot_id 与临时文件生命周期，不允许另建一套坐标基准。
    if (!vision) vision = new yiuk::vision::VisionService(this);

    // screen.capture：支持 max_width 缩放与 PNG/JPEG 落盘，但**仍然不回图片本体**：
    // 图片走既有的文件下载通路，与旧客户端的 take_screenshot 行为一致。
    nativeOps->registerOperation(
        QStringLiteral("screen.capture"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            yiuk::vision::CaptureOptions options;
            const QJsonValue maxWidth = arguments.value(QStringLiteral("max_width"));
            if (maxWidth.isDouble()) options.maxWidth = maxWidth.toInt();
            options.maxWidth = qBound(0, options.maxWidth, yiuk::vision::VisionService::kMaxOutputWidth);
            const QString format = arguments.value(QStringLiteral("format")).toString();
            if (!format.isEmpty()) options.format = format;
            // save 显式给 false 时只回几何（视觉查找不需要图片时用）。
            options.saveToDisk = arguments.value(QStringLiteral("save")).toBool(true);
            const QJsonValue quality = arguments.value(QStringLiteral("jpeg_quality"));
            if (quality.isDouble()) options.jpegQuality = quality.toInt();

            yiuk::vision::CaptureOutcome outcome;
            QString code;
            QString message;
            if (!vision->capture(sessionId, options, cancelled, &outcome, &code, &message)) {
                const QString hint = code == QLatin1String("SCREENSHOT_FAILED")
                                         ? QStringLiteral("确认设备未锁屏、没有停留在 UAC 安全桌面")
                                         : QString();
                return NativeOperationDispatcher::envelopeError(code, message, hint);
            }

            QJsonObject data = yiuk::vision::geometryToJson(outcome.geometry);
            data.insert(QStringLiteral("captured"), true);
            data.insert(QStringLiteral("screenshot_id"), outcome.screenshotId);
            data.insert(QStringLiteral("layout_version"), outcome.layoutVersion);
            data.insert(QStringLiteral("format"),
                        options.format.compare(QLatin1String("jpeg"), Qt::CaseInsensitive) == 0
                            || options.format.compare(QLatin1String("jpg"), Qt::CaseInsensitive) == 0
                            ? QStringLiteral("jpeg")
                            : QStringLiteral("png"));
            data.insert(QStringLiteral("size_bytes"), static_cast<double>(outcome.sizeBytes));
            // 只有落盘后才有 path；没有 path 时调用方不能下载图片，只能用几何。
            if (!outcome.filePath.isEmpty()) data.insert(QStringLiteral("path"), outcome.filePath);
            return NativeOperationDispatcher::envelopeOk(data);
        });

    // input.click：截图坐标 -> 物理坐标 -> 原生点击，可选点击前后的局部变化采样。
    // 具体流程在 VisionService::clickWithOptions，互操作程序也调同一份。
    nativeOps->registerOperation(
        QStringLiteral("input.click"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            QString button = arguments.value(QStringLiteral("button")).toString();
            if (button.isEmpty()) button = QStringLiteral("left");
            const int settleMs = qMax(0, arguments.value(QStringLiteral("settle_ms")).toInt());
            QJsonObject data;
            QString code;
            QString message;
            if (!vision->clickWithOptions(sessionId, arguments.value(QStringLiteral("screenshot_id")).toString(),
                                          arguments.value(QStringLiteral("x")).toInt(),
                                          arguments.value(QStringLiteral("y")).toInt(), button,
                                          arguments.value(QStringLiteral("double")).toBool(),
                                          arguments.value(QStringLiteral("detect_change")).toBool(), settleMs,
                                          cancelled, &data, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message, nativeHintForCode(code));
            return NativeOperationDispatcher::envelopeOk(data);
        });

    // input.move：duration_ms > 0 时按确定性 ease-in-out 分步移动（悬停菜单 / dock 需要中间 mousemove）。
    nativeOps->registerOperation(
        QStringLiteral("input.move"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            const int x = arguments.value(QStringLiteral("x")).toInt();
            const int y = arguments.value(QStringLiteral("y")).toInt();
            const QString screenshotId = arguments.value(QStringLiteral("screenshot_id")).toString();
            const int durationMs = qBound(0, arguments.value(QStringLiteral("duration_ms")).toInt(), 2000);
            const int settleMs = qBound(0, arguments.value(QStringLiteral("settle_ms")).toInt(), 2000);

            QString code;
            QString message;
            yiuk::vision::VisionService::DeviceLock deviceLock(*vision, cancelled, &code, &message);
            if (!deviceLock.held())
                return NativeOperationDispatcher::envelopeError(code, message);

            int screenX = 0;
            int screenY = 0;
            if (!vision->toScreenPoint(sessionId, screenshotId, x, y, &screenX, &screenY, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message, nativeHintForCode(code));

            const QString foreground = yiuk::input::foregroundTitle();
            QString error;
            if (!yiuk::input::move(screenX, screenY, durationMs, cancelled, &error)) {
                const bool cancelledByCaller = cancelled != nullptr && cancelled->load();
                return NativeOperationDispatcher::envelopeError(
                    cancelledByCaller ? QStringLiteral("CANCELLED") : QStringLiteral("INPUT_FAILED"), error,
                    nativeHintForCode(QStringLiteral("INPUT_FAILED")));
            }
            if (settleMs > 0 && !waitCancellable(settleMs, cancelled))
                return NativeOperationDispatcher::envelopeError(QStringLiteral("CANCELLED"),
                                                               QStringLiteral("操作已被取消"));

            QJsonObject data{
                {QStringLiteral("x"), x},
                {QStringLiteral("y"), y},
                {QStringLiteral("screen_x"), screenX},
                {QStringLiteral("screen_y"), screenY},
                {QStringLiteral("duration_ms"), durationMs},
                {QStringLiteral("settle_ms"), settleMs},
                {QStringLiteral("foreground_title"), foreground},
            };
            return NativeOperationDispatcher::envelopeOk(data);
        });

    // vision.find：UIA 文本定位 / OpenCV 模板匹配。坐标以最近一张截图为准，返回里的
    // screenshot_id 可以直接交给 click 或 drag。
    nativeOps->registerOperation(
        QStringLiteral("vision.find"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            return runVisionFind(sessionId, arguments, cancelled);
        });

    // vision.click_target：客户端内 find -> pick -> click 一笔事务（全程持设备门）。
    // 找不到 / 歧义 / 取消时一个 down 都不发，失败信封里带候选供 Agent 换参数。
    nativeOps->registerOperation(
        QStringLiteral("vision.click_target"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            return runVisionClickTarget(sessionId, arguments, cancelled);
        });

    // input.drag：按住左键沿确定性路径拖动（窗口、滑块、拖拽排序）。
    // 起终点都用截图坐标；按键抬起由 input::drag 的 guard 保证。
    nativeOps->registerOperation(
        QStringLiteral("input.drag"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
            const QString screenshotId = arguments.value(QStringLiteral("screenshot_id")).toString();
            const QJsonArray from = arguments.value(QStringLiteral("from")).toArray();
            const QJsonArray to = arguments.value(QStringLiteral("to")).toArray();
            if (from.size() != 2 || to.size() != 2)
                return NativeOperationDispatcher::envelopeError(QStringLiteral("INVALID_ARGUMENT"),
                                                               QStringLiteral("from 与 to 必须是 [x,y] 两个整数"));
            const int durationMs = qBound(50, arguments.value(QStringLiteral("duration_ms")).toInt(400), 5000);
            const int holdMs = qBound(0, arguments.value(QStringLiteral("hold_ms")).toInt(50), 1000);

            QString code;
            QString message;
            // 拖动会在中途持续占用鼠标，必须与抓屏/点击共用设备门。
            yiuk::vision::VisionService::DeviceLock deviceLock(*vision, cancelled, &code, &message);
            if (!deviceLock.held()) return NativeOperationDispatcher::envelopeError(code, message);

            int screenFromX = 0;
            int screenFromY = 0;
            int screenToX = 0;
            int screenToY = 0;
            if (!vision->toScreenPoint(sessionId, screenshotId, from.at(0).toInt(), from.at(1).toInt(),
                                       &screenFromX, &screenFromY, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message, nativeHintForCode(code));
            if (!vision->toScreenPoint(sessionId, screenshotId, to.at(0).toInt(), to.at(1).toInt(), &screenToX,
                                       &screenToY, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message, nativeHintForCode(code));

            QString error;
            if (!yiuk::input::drag(screenFromX, screenFromY, screenToX, screenToY, durationMs, holdMs, cancelled,
                                   &error)) {
                const bool cancelledByCaller = cancelled != nullptr && cancelled->load();
                return NativeOperationDispatcher::envelopeError(
                    cancelledByCaller ? QStringLiteral("CANCELLED") : QStringLiteral("INPUT_FAILED"), error,
                    nativeHintForCode(QStringLiteral("INPUT_FAILED")));
            }
            return NativeOperationDispatcher::envelopeOk(QJsonObject{
                {QStringLiteral("from"), QJsonArray{from.at(0).toInt(), from.at(1).toInt()}},
                {QStringLiteral("to"), QJsonArray{to.at(0).toInt(), to.at(1).toInt()}},
                {QStringLiteral("screen_from"), QJsonArray{screenFromX, screenFromY}},
                {QStringLiteral("screen_to"), QJsonArray{screenToX, screenToY}},
                {QStringLiteral("duration_ms"), durationMs},
                {QStringLiteral("hold_ms"), holdMs},
                {QStringLiteral("released"), true},
            });
        });

    // screen.release：MCP 下载完截图后让设备删掉临时文件。只删截图目录内的文件；
    // 快照元数据保留，screenshot_id 仍然可以作为坐标基准。
    nativeOps->registerOperation(
        QStringLiteral("screen.release"),
        [this](const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *) {
            const QString path = arguments.value(QStringLiteral("path")).toString();
            QString code;
            QString message;
            if (!vision->releaseScreenshotFile(sessionId, path, &code, &message))
                return NativeOperationDispatcher::envelopeError(code, message);
            return NativeOperationDispatcher::envelopeOk(QJsonObject{{QStringLiteral("released"), true}});
        });

    connect(nativeOps, &NativeOperationDispatcher::activity, this,
            [this](const QString &sessionId, const QString &summary) {
        appendActivity(sessionId, summary, ActivityKind::Execute);
    });
    connect(nativeOps, &NativeOperationDispatcher::finished, this,
            [this](const QString &sessionId, const QString &requestId, const QString &status,
                   const QJsonObject &envelope) {
        nativeRequestIds.remove(requestId);
        if (status == QLatin1String("cancelled") || status == QLatin1String("timed_out")) {
            // 平台已经把任务当成"未确认"：只回报终态，不再发信封。
            if (platform && platform->isConnected())
                platform->sendCommandResult(sessionId, requestId, status, QJsonValue(), true, false);
            refreshSessionsPage();
            return;
        }
        sendNativeEnvelope(sessionId, requestId, envelope);
    });
}

// sendNativeEnvelope 把业务信封写成一条 stdout 输出，再补终态。
// 任务成功只表示"设备把操作跑完了"，业务失败（NOT_FOUND 等）在信封里。
void MainWindow::sendNativeEnvelope(const QString &sessionId, const QString &requestId,
                                    const QJsonObject &envelope) {
    if (!platform || !platform->isConnected()) return;
    const QByteArray payload = QJsonDocument(envelope).toJson(QJsonDocument::Compact);
    const QString text = QString::fromUtf8(payload);
    const quint64 sequence = nextPlatformOutputSequenceByRequest.value(requestId, 1);
    nextPlatformOutputSequenceByRequest.insert(requestId, sequence + 1);
    platform->sendCommandOutput(sessionId, requestId, QStringLiteral("stdout"), text, sequence);
    platform->sendCommandResult(sessionId, requestId, QStringLiteral("succeeded"), QJsonValue(), true, false);
    nextPlatformOutputSequenceByRequest.remove(requestId);
    appendActivity(sessionId, QString("原生操作 %1 完成：%2").arg(requestId.left(8), text.left(400)),
                   ActivityKind::Output);
    refreshSessionsPage();
    refreshActiveSessions();
}

void MainWindow::onPlatformNativeRequested(const QString &sessionId, const QString &requestId,
                                           const QString &operation, const QJsonObject &arguments,
                                           int timeoutSeconds) {
    if (requestId.isEmpty() || !nativeOps) return;
    if (nativeRequestIds.contains(requestId)) return;
    const QString sessionState = sessions.value(sessionId).value("status").toString();
    if (sessionState != QLatin1String("active") && sessionState != QLatin1String("approved")) {
        if (platform && platform->isConnected())
            platform->sendCommandResult(sessionId, requestId, QStringLiteral("cancelled"), QJsonValue(), true, false);
        return;
    }
    if (!nativeOps->supports(operation)) {
        // 平台侧白名单比客户端新（客户端未升级）：明确回 UNSUPPORTED_OPERATION，
        // 不静默丢弃，否则 Agent 只能等超时。
        sendNativeEnvelope(sessionId, requestId,
                           NativeOperationDispatcher::envelopeError(
                               QStringLiteral("UNSUPPORTED_OPERATION"),
                               QStringLiteral("客户端未实现操作 %1").arg(operation),
                               QStringLiteral("升级 Qt 客户端到与平台相同的发布批次")));
        return;
    }
    nativeRequestIds.insert(requestId);
    appendActivity(sessionId, QString("Agent 请求原生操作 %1").arg(operation), ActivityKind::Execute);
    if (!nativeOps->start(sessionId, requestId, operation, arguments, timeoutSeconds)) {
        nativeRequestIds.remove(requestId);
        sendNativeEnvelope(sessionId, requestId,
                           NativeOperationDispatcher::envelopeError(
                               QStringLiteral("NATIVE_BUSY"), QStringLiteral("同名请求已在执行"),
                               QStringLiteral("等待上一个请求结束后重试")));
    }
}

void MainWindow::onPlatformCommandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                                const QString &workingDirectory, int timeoutSeconds) {
    if (requestId.isEmpty() || commandRunners.contains(requestId)) return;
    const QString sessionState = sessions.value(sessionId).value("status").toString();
    if (sessionState != QLatin1String("active") && sessionState != QLatin1String("approved")) {
        if (platform && platform->isConnected())
            platform->sendCommandResult(sessionId, requestId, "cancelled", QJsonValue(), true, false);
        return;
    }
    appendActivity(sessionId, QString("Agent 开始执行任务 %1\nPowerShell> %2").arg(requestId.left(8), command),
                   ActivityKind::Execute);
    startPlatformCommand(sessionId, requestId, command, workingDirectory, timeoutSeconds);
}


void MainWindow::startPlatformCommand(const QString &sessionId, const QString &requestId, const QString &command,
                            const QString &workingDirectory, int timeoutSeconds) {
    auto sendFailure = [this, sessionId, requestId](const QString &message) {
        QJsonObject commandTask{{"session_id", sessionId}, {"request_id", requestId}, {"status", "failed"},
                         {"stdout", ""}, {"stderr", message}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}};
        tasks.insert(requestId, commandTask);
        if (platform && platform->isConnected())
            platform->sendCommandResult(sessionId, requestId, "failed", QJsonValue(), true, false);
        refreshSessionsPage();
    };
    if (command.trimmed().isEmpty() || command.size() > 32768) {
        sendFailure("命令为空或超过 32 KiB");
        return;
    }
    const QString effectiveWorkingDirectory = workingDirectory.trimmed().isEmpty()
        ? defaultCommandWorkingDirectory() : workingDirectory.trimmed();

    PowerShellRunner::Callbacks callbacks;
    callbacks.onOutput = [this, requestId, sessionId](const QByteArray &retainedOutput, bool isStandardError, bool truncated) {
        QJsonObject commandTask = tasks.value(requestId);
        const QString outputField = isStandardError ? QStringLiteral("stderr") : QStringLiteral("stdout");
        const QString output = QString::fromUtf8(retainedOutput);
        commandTask.insert(outputField, commandTask.value(outputField).toString() + output);
        commandTask.insert("truncated", truncated);
        tasks.insert(requestId, commandTask);
        appendActivity(sessionId, output.left(4000),
                       isStandardError ? ActivityKind::Error : ActivityKind::Output);
        if (platform && platform->isConnected() && !retainedOutput.isEmpty()) {
            const quint64 sequence = nextPlatformOutputSequenceByRequest.value(requestId, 1);
            nextPlatformOutputSequenceByRequest.insert(requestId, sequence + 1);
            platform->sendCommandOutput(sessionId, requestId, isStandardError ? "stderr" : "stdout", output, sequence);
        }
    };
    callbacks.onFinished = [this, requestId, sessionId](const QString &status, int exitCode, const QString &error) {
        if (PowerShellRunner *runner = commandRunners.take(requestId)) runner->deleteLater();
        nextPlatformOutputSequenceByRequest.remove(requestId);
        platformSessionByRequest.remove(requestId);
        QJsonObject commandTask = tasks.value(requestId);
        if (status == QLatin1String("timed_out")) {
            commandTask.insert("status", "timed_out");
            tasks.insert(requestId, commandTask);
            appendActivity(sessionId, QString("任务 %1 已超时，正在停止 PowerShell").arg(requestId.left(8)),
                           ActivityKind::Error);
            persistSessionState();
            if (platform && platform->isConnected())
                platform->sendCommandResult(sessionId, requestId, "timed_out", QJsonValue(), false, commandTask.value("truncated").toBool());
            refreshSessionsPage();
            refreshActiveSessions();
            return;
        }
        if (status == QLatin1String("cancelled")) {
            // 发起取消的调用方已经记录了最终状态
            if (commandTask.value("status").toString() == QLatin1String("running"))
                commandTask.insert("status", "cancelled");
            tasks.insert(requestId, commandTask);
            persistSessionState();
            refreshSessionsPage();
            refreshActiveSessions();
            return;
        }
        if (!error.isEmpty()) {
            commandTask.insert("status", "failed");
            commandTask.insert("stderr", commandTask.value("stderr").toString() + error);
            tasks.insert(requestId, commandTask);
            persistSessionState();
            if (platform && platform->isConnected())
                platform->sendCommandResult(sessionId, requestId, "failed", QJsonValue(), true, false);
            refreshSessionsPage();
            refreshActiveSessions();
            return;
        }
        commandTask.insert("status", status);
        commandTask.insert("exit_code", exitCode);
        tasks.insert(requestId, commandTask);
        persistSessionState();
        if (platform && platform->isConnected())
            platform->sendCommandResult(sessionId, requestId, status, exitCode, true, commandTask.value("truncated").toBool());
        refreshSessionsPage();
        refreshActiveSessions();
        showStatusMessage("平台命令任务完成", 3000);
    };

    tasks.insert(requestId, {{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"},
                             {"stdout", ""}, {"stderr", ""}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}});
    platformSessionByRequest.insert(requestId, sessionId);
    nextPlatformOutputSequenceByRequest.insert(requestId, 1);
    appendActivity(sessionId, QString("本机已启动 PowerShell 任务 %1").arg(requestId.left(8)));

    auto *runner = new PowerShellRunner(this, requestId, command, effectiveWorkingDirectory, timeoutSeconds, callbacks);
    commandRunners.insert(requestId, runner);
    QString startError;
    if (!runner->start(&startError)) {
        commandRunners.remove(requestId);
        runner->deleteLater();
        tasks.remove(requestId);
        platformSessionByRequest.remove(requestId);
        nextPlatformOutputSequenceByRequest.remove(requestId);
        sendFailure(startError);
        return;
    }
    showStatusMessage("正在执行平台下发任务", 3000);
    refreshActiveSessions();
}
