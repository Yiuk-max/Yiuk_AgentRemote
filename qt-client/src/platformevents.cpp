#include "mainwindow.h"

#include "powershellrunner.h"


// adoptDeviceSession records a session where this client is the assisted device,
// including direct mode where the platform skips the approval request.
void MainWindow::adoptDeviceSession(const QJsonObject &session, bool approved) {
    const QString id = session.value("session_id").toString();
    if (id.isEmpty()) return;
    const bool fresh = !sessions.contains(id);
    QJsonObject record = session;
    record.insert("source", "platform");
    if (!record.contains("expires_at_ms") && record.value("expires_at").isDouble())
        record.insert("expires_at_ms", record.value("expires_at").toDouble() * 1000.0);
    if (record.value("requester").toString().isEmpty())
        record.insert("requester", session.value("operator").toString("平台操作者"));
    if (record.value("source_device").toString().isEmpty())
        record.insert("source_device", session.value("operator_device").toString());
    sessions.insert(id, record);
    if (fresh) {
        appendActivity(id, approved
            ? QString("Agent 已直接连接（无需批准）：%1").arg(record.value("purpose").toString())
            : QString("收到 Agent 协助请求：%1").arg(record.value("purpose").toString()));
        notificationSessions.insert(id, record.value("purpose").toString());
    }
    persistSessionState();
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
    if (activitySessionSelector) refreshActivityWindow();
    showStatusMessage(approved ? "已建立直接连接会话" : "有待批准的协助请求", 4000);
    if (fresh && trayIcon && QSystemTrayIcon::supportsMessages()) {
        trayIcon->showMessage(approved ? "远程协助已连接" : "需要批准远程协助",
                              record.value("purpose").toString(),
                              QSystemTrayIcon::Information, 10000);
    }
}


void MainWindow::onPlatformSessionRequested(const QJsonObject &session) {
    const QString id = session.value("session_id").toString();
    if (id.isEmpty()) return;
    adoptDeviceSession(session, false);
    if (trayIcon && QSystemTrayIcon::supportsMessages()) return;
    QTimer::singleShot(0, this, [this, id] { showApprovalDialog(id); });
}


void MainWindow::onPlatformSessionUpdated(const QJsonObject &session) {
    const QString id = session.value("session_id").toString();
    if (id.isEmpty()) return;
    const QJsonObject existing = sessions.value(id);
    const bool known = existing.contains("source");
    QJsonObject record = session;
    if (!record.contains("source")) {
        if (known) {
            record.insert("source", existing.value("source"));
        } else if (session.value("device_id").toString() == deviceId()) {
            // direct 模式：平台直接下发已批准会话，没有 session_request 阶段。
            adoptDeviceSession(session, session.value("status").toString() == "approved");
            return;
        } else {
            record.insert("source", "platform-operator");
        }
    }
    if (!record.contains("expires_at_ms") && record.value("expires_at").isDouble())
        record.insert("expires_at_ms", record.value("expires_at").toDouble() * 1000.0);
    if (record.value("source").toString() == QLatin1String("platform")) {
        if (record.value("requester").toString().isEmpty())
            record.insert("requester", record.value("operator").toString("平台操作者"));
        if (record.value("source_device").toString().isEmpty())
            record.insert("source_device", record.value("operator_device").toString());
    }
    sessions.insert(id, record);
    appendActivity(id, QString("平台会话状态：%1").arg(session.value("status").toString()));
    persistSessionState();
    const QString status = session.value("status").toString();
    if (status != "active" && status != "approved" && status != "pending_approval")
        stopTasksForSession(id, status == "closed" ? "cancelled" : "result_unknown");
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
    if (activitySessionSelector) refreshActivityWindow();
}


void MainWindow::onPlatformSessionCreated(const QJsonObject &session) {
    if (session.value("device_id").toString() == deviceId()) {
        adoptDeviceSession(session, session.value("status").toString() == "approved");
        return;
    }
    const QString id = session.value("session_id").toString();
    if (id.isEmpty()) return;
    QJsonObject record = session;
    record.insert("source", "platform-operator");
    if (!record.contains("expires_at_ms") && record.value("expires_at").isDouble())
        record.insert("expires_at_ms", record.value("expires_at").toDouble() * 1000.0);
    sessions.insert(id, record);
    persistSessionState();
    refreshSessionsPage();
    const QString status = session.value("status").toString();
    showStatusMessage(status == "approved" ? "会话已批准" : "已发送协助请求，等待对方批准", 4000);
}


void MainWindow::onPlatformCloseRequested(const QString &sessionId) {
    appendActivity(sessionId, "平台已请求结束会话");
    stopTasksForSession(sessionId, "cancelled");
    QJsonObject session = sessions.value(sessionId);
    session.insert("status", "closed");
    sessions.insert(sessionId, session);
    persistSessionState();
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
    if (activitySessionSelector) refreshActivityWindow();
}


void MainWindow::onPlatformCancelRequested(const QString &sessionId, const QString &requestId) {
    appendActivity(sessionId, QString("Agent 请求停止任务：%1").arg(requestId.left(8)));
    QJsonObject task = tasks.value(requestId);
    task.insert("status", "cancelled");
    task.insert("exit_code", QJsonValue(QJsonValue::Null));
    tasks.insert(requestId, task);
    persistSessionState();
    if (PowerShellRunner *runner = runs.value(requestId)) runner->cancel();
    platformOutputSequence.remove(requestId);
    platformTaskSession.remove(requestId);
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


void MainWindow::finishFileDownload(const QSharedPointer<FileDownloadState> &state, const QString &status,
                        bool resultKnown) {
    if (!state || state->requestId.isEmpty()) return;
    const QString sha = status == QLatin1String("succeeded") && state->hash
        ? QString::fromLatin1(state->hash->result().toHex()) : QString();
    if (platform && platform->isConnected()) {
        platform->sendFileDownloadResult(state->sessionId, state->requestId, status, state->fileName,
                                         state->sent, sha, resultKnown);
    }
    activeFileDownloads.remove(state->requestId);
    if (state->file) {
        state->file->close();
        state->file->deleteLater();
        state->file = nullptr;
    }
    delete state->hash;
    state->hash = nullptr;
    refreshActiveSessions();
}


void MainWindow::pumpFileDownload(const QSharedPointer<FileDownloadState> &state) {
    if (!state) return;
    if (!activeFileDownloads.contains(state->requestId)) {
        if (state->file) {
            state->file->close();
            state->file->deleteLater();
        }
        delete state->hash;
        return;
    }
    if (!sessionAllowsFileRead(state->sessionId)) {
        appendActivity(state->sessionId, QString("文件下载 %1 已停止：会话不再授权读取").arg(state->requestId.left(8)));
        finishFileDownload(state, QStringLiteral("cancelled"), true);
        return;
    }
    if (state->file->atEnd()) {
        finishFileDownload(state, QStringLiteral("succeeded"), true);
        return;
    }
    constexpr qint64 chunkSize = 64 * 1024;
    const QByteArray chunk = state->file->read(chunkSize);
    if (chunk.isEmpty() && state->file->error() != QFile::NoError) {
        appendActivity(state->sessionId, QString("文件下载 %1 读取失败：%2").arg(state->requestId.left(8), state->file->errorString()));
        finishFileDownload(state, QStringLiteral("failed"), true);
        return;
    }
    constexpr qint64 maxDownloadBytes = 32LL * 1024 * 1024;
    if (state->sent + chunk.size() > maxDownloadBytes) {
        appendActivity(state->sessionId, QString("文件下载 %1 已停止：文件超过 32 MiB 上限").arg(state->requestId.left(8)));
        finishFileDownload(state, QStringLiteral("failed"), true);
        return;
    }
    state->hash->addData(chunk);
    state->sent += chunk.size();
    const bool eof = state->file->atEnd();
    if (platform && platform->isConnected()) {
        platform->sendFileChunk(state->sessionId, state->requestId, state->sequence++, chunk, eof);
    }
    if (eof) {
        if (state->expected >= 0 && state->sent != state->expected) {
            appendActivity(state->sessionId, QString("文件下载 %1 失败：读取大小与预期不一致").arg(state->requestId.left(8)));
            finishFileDownload(state, QStringLiteral("failed"), true);
            return;
        }
        appendActivity(state->sessionId, QString("文件下载 %1 完成：%2 字节").arg(state->requestId.left(8)).arg(state->sent));
        finishFileDownload(state, QStringLiteral("succeeded"), true);
        return;
    }
    QTimer::singleShot(0, this, [this, state] { pumpFileDownload(state); });
}


void MainWindow::onPlatformFileDownloadRequested(const QString &sessionId, const QString &requestId,
                                     const QString &remotePath) {
    auto fail = [this, sessionId, requestId](const QString &message) {
        appendActivity(sessionId, QString("文件下载 %1 被拒绝：%2").arg(requestId.left(8), message));
        if (platform && platform->isConnected())
            platform->sendFileDownloadResult(sessionId, requestId, QStringLiteral("failed"), QString(), 0, QString(), true);
    };
    if (requestId.isEmpty() || activeFileDownloads.contains(requestId)) return;
    if (!sessionAllowsFileRead(sessionId)) {
        fail(QStringLiteral("会话未获得 read_file 权限或已结束"));
        return;
    }
    const QString path = remotePath.trimmed();
    if (path.isEmpty() || path.size() > 4096) {
        fail(QStringLiteral("远程路径为空或过长"));
        return;
    }
    QFileInfo info(path);
    if (!info.exists() || !info.isFile()) {
        fail(QStringLiteral("远程路径不是可读取的普通文件"));
        return;
    }
    constexpr qint64 maxDownloadBytes = 32LL * 1024 * 1024;
    if (info.size() > maxDownloadBytes) {
        fail(QStringLiteral("文件超过 32 MiB 下载上限"));
        return;
    }
    auto *file = new QFile(path, this);
    if (!file->open(QIODevice::ReadOnly)) {
        const QString error = file->errorString();
        file->deleteLater();
        fail(QStringLiteral("无法打开文件：%1").arg(error));
        return;
    }
    auto state = QSharedPointer<FileDownloadState>::create();
    state->sessionId = sessionId;
    state->requestId = requestId;
    state->fileName = info.fileName();
    state->file = file;
    state->hash = new QCryptographicHash(QCryptographicHash::Sha256);
    state->expected = info.size();
    activeFileDownloads.insert(requestId, true);
    appendActivity(sessionId, QString("Agent 请求下载文件 %1（%2 字节）").arg(state->fileName).arg(info.size()));
    showActivityWindow(sessionId);
    refreshActiveSessions();
    QTimer::singleShot(0, this, [this, state] { pumpFileDownload(state); });
}


void MainWindow::onPlatformCommandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                                const QString &workingDirectory, int timeoutSeconds) {
    appendActivity(sessionId, QString("Agent 开始执行任务 %1\nPowerShell> %2").arg(requestId.left(8), command));
    showActivityWindow(sessionId);
    executePlatformCommand(sessionId, requestId, command, workingDirectory, timeoutSeconds);
}


void MainWindow::executePlatformCommand(const QString &sessionId, const QString &requestId, const QString &command,
                            const QString &workingDirectory, int timeoutSeconds) {
    auto sendFailure = [this, sessionId, requestId](const QString &message) {
        QJsonObject task{{"session_id", sessionId}, {"request_id", requestId}, {"status", "failed"},
                         {"stdout", ""}, {"stderr", message}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}};
        tasks.insert(requestId, task);
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

    PowerShellRunner::Hooks hooks;
    hooks.onOutput = [this, requestId, sessionId](const QByteArray &accepted, bool standardError, bool truncated) {
        QJsonObject task = tasks.value(requestId);
        const QString key = standardError ? QStringLiteral("stderr") : QStringLiteral("stdout");
        const QString output = QString::fromUtf8(accepted);
        task.insert(key, task.value(key).toString() + output);
        task.insert("truncated", truncated);
        tasks.insert(requestId, task);
        appendActivity(sessionId, QString("%1> %2").arg(standardError ? "stderr" : "stdout", output.left(4000)));
        if (platform && platform->isConnected() && !accepted.isEmpty()) {
            const quint64 sequence = platformOutputSequence.value(requestId, 1);
            platformOutputSequence.insert(requestId, sequence + 1);
            platform->sendCommandOutput(sessionId, requestId, standardError ? "stderr" : "stdout", output, sequence);
        }
    };
    hooks.onFinished = [this, requestId, sessionId](const QString &status, int exitCode, const QString &error) {
        if (PowerShellRunner *runner = runs.take(requestId)) runner->deleteLater();
        platformOutputSequence.remove(requestId);
        platformTaskSession.remove(requestId);
        QJsonObject task = tasks.value(requestId);
        if (status == QLatin1String("timed_out")) {
            task.insert("status", "timed_out");
            tasks.insert(requestId, task);
            appendActivity(sessionId, QString("任务 %1 已超时，正在停止 PowerShell").arg(requestId.left(8)));
            persistSessionState();
            if (platform && platform->isConnected())
                platform->sendCommandResult(sessionId, requestId, "timed_out", QJsonValue(), false, task.value("truncated").toBool());
            refreshSessionsPage();
            refreshActiveSessions();
            return;
        }
        if (status == QLatin1String("cancelled")) {
            // The cancelling caller already recorded the terminal status.
            if (task.value("status").toString() == QLatin1String("running"))
                task.insert("status", "cancelled");
            tasks.insert(requestId, task);
            persistSessionState();
            refreshSessionsPage();
            refreshActiveSessions();
            return;
        }
        if (!error.isEmpty()) {
            task.insert("status", "failed");
            task.insert("stderr", task.value("stderr").toString() + error);
            tasks.insert(requestId, task);
            persistSessionState();
            if (platform && platform->isConnected())
                platform->sendCommandResult(sessionId, requestId, "failed", QJsonValue(), true, false);
            refreshSessionsPage();
            refreshActiveSessions();
            return;
        }
        task.insert("status", status);
        task.insert("exit_code", exitCode);
        tasks.insert(requestId, task);
        persistSessionState();
        if (platform && platform->isConnected())
            platform->sendCommandResult(sessionId, requestId, status, exitCode, true, task.value("truncated").toBool());
        refreshSessionsPage();
        refreshActiveSessions();
        showStatusMessage("平台命令任务完成", 3000);
    };

    tasks.insert(requestId, {{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"},
                             {"stdout", ""}, {"stderr", ""}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}});
    platformTaskSession.insert(requestId, sessionId);
    platformOutputSequence.insert(requestId, 1);
    appendActivity(sessionId, QString("本机已启动 PowerShell 任务 %1").arg(requestId.left(8)));

    auto *runner = new PowerShellRunner(this, requestId, command, effectiveWorkingDirectory, timeoutSeconds, hooks);
    runs.insert(requestId, runner);
    QString startError;
    if (!runner->start(&startError)) {
        runs.remove(requestId);
        runner->deleteLater();
        tasks.remove(requestId);
        platformTaskSession.remove(requestId);
        platformOutputSequence.remove(requestId);
        sendFailure(startError);
        return;
    }
    showStatusMessage("正在执行平台下发任务", 3000);
    refreshActiveSessions();
}
