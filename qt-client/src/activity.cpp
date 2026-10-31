#include "mainwindow.h"

#include "yiukdialog.h"


// 活动面板始终显示当前所属的会话：优先显示活动会话，否则显示最近结束的会话
QString MainWindow::currentActivitySession() const {
    QString liveId;
    QString recentId;
    qint64 liveCreated = -1;
    qint64 recentCreated = -1;
    for (auto it = sessions.cbegin(); it != sessions.cend(); ++it) {
        const QJsonObject session = it.value();
        if (session.value("source").toString() != QLatin1String("platform")) continue;
        const qint64 created = static_cast<qint64>(session.value("created_at_ms").toDouble());
        const QString status = session.value("status").toString();
        if (status == QLatin1String("active") || status == QLatin1String("approved") ||
            status == QLatin1String("pending_approval")) {
            if (created >= liveCreated) {
                liveCreated = created;
                liveId = it.key();
            }
        }
        if (created >= recentCreated) {
            recentCreated = created;
            recentId = it.key();
        }
    }
    return liveId.isEmpty() ? recentId : liveId;
}


QString MainWindow::activitySessionLabel(const QString &sessionId) const {
    if (sessionId.isEmpty() || !sessions.contains(sessionId)) return QStringLiteral("当前没有会话");
    const QJsonObject session = sessions.value(sessionId);
    QString name = session.value("requester").toString().trimmed();
    if (name.isEmpty()) name = QStringLiteral("Agent");
    QString device = session.value("source_device").toString().trimmed();
    if (device.isEmpty()) device = session.value("operator_device").toString().trimmed();
    if (device.isEmpty()) return name;
    QString digits;
    for (const QChar ch : device) if (ch.isDigit()) digits += ch;
    return name + QStringLiteral(" · ") + (digits.size() == 9 ? spacedCode(digits) : device);
}


QString MainWindow::activitySessionState(const QString &sessionId) const {
    if (sessionId.isEmpty() || !sessions.contains(sessionId)) return QStringLiteral("none");
    const QString status = sessions.value(sessionId).value("status").toString();
    if (status == QLatin1String("active") || status == QLatin1String("approved"))
        return QStringLiteral("active");
    if (status == QLatin1String("pending_approval")) return QStringLiteral("pending");
    return QStringLiteral("closed");
}


void MainWindow::appendActivity(const QString &sessionId, const QString &message, ActivityKind kind) {
    if (sessionId.isEmpty() || message.isEmpty()) return;
    QVector<ActivityEntry> &log = activityLogs[sessionId];
    ActivityEntry entry;
    entry.kind = kind;
    entry.time = QDateTime::currentDateTime().toString(QStringLiteral("HH:mm:ss"));
    entry.text = message;
    log.append(entry);
    while (log.size() > 400) log.removeFirst();
    // 面板关闭后不会因新活动而重新打开或置顶；只有所有者能从活动会话卡片或托盘菜单打开面板
    if (!activityPanel || !activityPanel->isOpen() || activityPanel->sessionId() != sessionId) return;
    activityPanel->append(entry);
    activityPanel->setStatus(activitySessionState(sessionId), sessionStatusText(sessionId));
}


void MainWindow::syncActivityPanel() {
    if (!activityPanel || !activityPanel->isOpen()) return;
    const QString id = currentActivitySession();
    if (id.isEmpty()) {
        activityPanel->setStatus(QStringLiteral("none"), QStringLiteral("当前没有远程协助会话"));
        return;
    }
    // 切换内容只是更新数据；窗口保持原有层级和焦点，不会因新活动而遮挡所有者正在进行的工作
    if (activityPanel->sessionId() != id) {
        activityPanel->setSessionId(id);
        activityPanel->reset(activitySessionLabel(id), activitySessionState(id), sessionStatusText(id),
                             activityLogs.value(id));
        return;
    }
    activityPanel->setStatus(activitySessionState(id), sessionStatusText(id));
}


void MainWindow::showActivityWindow(const QString &sessionId) {
    const QString id = sessionId.isEmpty() ? currentActivitySession() : sessionId;
    if (!activityPanel) activityPanel = new ActivityPanel(this);
    if (activityPanel->isOpen()) {
        syncActivityPanel();
        activityPanel->activate();
        return;
    }
    const QString label = id.isEmpty() ? QStringLiteral("当前没有会话") : activitySessionLabel(id);
    const QString state = activitySessionState(id);
    const QString status = id.isEmpty() ? QStringLiteral("当前没有远程协助会话") : sessionStatusText(id);
    activityPanel->setSessionId(id);
    if (!activityPanel->open(QStringLiteral("远程协助活动"), label, state, status, activityLogs.value(id))) {
        showInfoMessage(this, QStringLiteral("信息面板无法启动"),
                        QStringLiteral("找不到 yiuk-activity-panel.exe。请重新安装客户端，"
                                       "或确认它与主程序位于同一目录。"));
    }
}
