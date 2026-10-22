#include "mainwindow.h"

#include "powershellrunner.h"
#include "yiukdialog.h"


void MainWindow::expireSessionIfNeeded(const QString &id) {
    if (!sessions.contains(id)) return;
    QJsonObject session = sessions.value(id);
    const QString status = session.value("status").toString();
    const qint64 expiresAt = static_cast<qint64>(session.value("expires_at_ms").toDouble());
    if ((status == "pending_approval" || status == "approved" || status == "active") && expiresAt > 0 && QDateTime::currentMSecsSinceEpoch() >= expiresAt) {
        session.insert("status", "expired");
        sessions.insert(id, session);
        persistSessionState();
        stopTasksForSession(id, "cancelled");
        refreshPendingRequests();
        refreshActiveSessions();
    }
}


void MainWindow::stopTasksForSession(const QString &id, const QString &status) {
    QStringList requestIds;
    for (auto it = tasks.cbegin(); it != tasks.cend(); ++it) {
        if (it.value().value("session_id").toString() == id && runs.contains(it.key()))
            requestIds.append(it.key());
    }
    for (const QString &requestId : requestIds) {
        // Record the terminal status first: the runner reports "cancelled" to the
        // finished hook, which keeps a status the caller already set.
        QJsonObject task = tasks.value(requestId);
        task.insert("status", status);
        tasks.insert(requestId, task);
        if (PowerShellRunner *runner = runs.value(requestId)) runner->cancel();
    }
}


void MainWindow::refreshPendingRequests() {
    while (QLayoutItem *item = pendingLayout->takeAt(0)) {
        if (item->widget()) item->widget()->deleteLater();
        delete item;
    }
    for (auto it = sessions.cbegin(); it != sessions.cend(); ++it) {
        expireSessionIfNeeded(it.key());
        const QJsonObject session = sessions.value(it.key());
        if (session.value("status").toString() != "pending_approval") continue;
        auto *card = new QFrame;
        card->setStyleSheet("QFrame { background: #fff9e8; border: 1px solid #f2d58a; border-radius: 7px; }");
        auto *row = new QHBoxLayout(card);
        row->setContentsMargins(12, 9, 10, 9);
        const QString permissionText = jsonArrayText(session.value("permissions").toArray());
        const qint64 remaining = qMax<qint64>(0, (static_cast<qint64>(session.value("expires_at_ms").toDouble()) - QDateTime::currentMSecsSinceEpoch()) / 1000);
        const QString details = QString("请求者：%1  ·  来源：%2\n请求时间：%3  ·  剩余：%4 秒\n用途：%5\n权限：%6")
            .arg(session.value("requester").toString(), session.value("source_device").toString(),
                 session.value("created_at").toString(), QString::number(remaining),
                 session.value("purpose").toString(), permissionText);
        auto *text = makeLabel(details, "color: #49566a; font-size: 12px;");
        text->setWordWrap(true);
        row->addWidget(text, 1);
        auto *approve = makePrimaryButton(QStringLiteral("批准"), 32);
        auto *reject = makeButton(QStringLiteral("拒绝"));
        reject->setObjectName("subtle");
        row->addWidget(approve);
        row->addWidget(reject);
        const QString id = it.key();
        connect(approve, &QPushButton::clicked, this, [this, id] { decideSession(id, true); });
        connect(reject, &QPushButton::clicked, this, [this, id] { decideSession(id, false); });
        pendingLayout->addWidget(card);
    }
}


void MainWindow::showApprovalDialog(const QString &id) {
    if (!sessions.contains(id) || sessions.value(id).value("status").toString() != "pending_approval") return;
    expireSessionIfNeeded(id);
    if (sessions.value(id).value("status").toString() != "pending_approval") return;
    const QJsonObject session = sessions.value(id);
    // Frameless blue/white approval sheet: same title bar and buttons as the
    // main window, no native Windows dialog chrome.
    YiukDialog dialog(this, QStringLiteral("远程协助授权"));
    dialog.setFixedWidth(560);
    dialog.content()->addWidget(makeLabel(QStringLiteral("Agent 请求远程协助权限"),
                                          "font-size: 15px; font-weight: 650; color: #263446;"));
    const QString permissionText = jsonArrayText(session.value("permissions").toArray());
    auto *details = makeLabel(QString("请求者：%1\n来源设备：%2\n请求时间：%3\n用途：%4\n请求权限：%5\n有效期至：%6\n\n批准后，Agent 可在当前 Windows 用户权限范围内执行多条 PowerShell 命令，或按 read_file 权限下载远端文件。普通用户权限不是沙箱。请求者身份尚未经过远程验证。")
        .arg(session.value("requester").toString(), session.value("source_device").toString(),
             session.value("created_at").toString(), session.value("purpose").toString(),
             permissionText, session.value("expires_at").toString()),
        "font-size: 12px; color: #49566a;");
    details->setTextFormat(Qt::PlainText);
    details->setWordWrap(true);
    details->setTextInteractionFlags(Qt::TextSelectableByMouse);
    dialog.content()->addWidget(details);
    dialog.content()->addStretch();
    auto *reject = dialog.addButton(QStringLiteral("拒绝"));
    auto *approve = dialog.addButton(QStringLiteral("批准本次会话"), YiukDialog::ButtonKind::Primary);
    approve->setDefault(true);
    QObject::connect(reject, &QPushButton::clicked, &dialog, &QDialog::reject);
    QObject::connect(approve, &QPushButton::clicked, &dialog, &QDialog::accept);
    dialog.adjustSize();
    const bool approved = dialog.exec() == QDialog::Accepted;
    decideSession(id, approved);
    showNormal();
    activateWindow();
}


void MainWindow::decideSession(const QString &id, bool approved) {
    if (!sessions.contains(id)) return;
    QJsonObject session = sessions.value(id);
    if (session.value("status").toString() != "pending_approval") return;
    session.insert("status", approved ? "active" : "rejected");
    session.insert("decided_at", QDateTime::currentDateTimeUtc().toString(Qt::ISODate));
    sessions.insert(id, session);
    persistSessionState();
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
    const bool platformSession = session.value("source").toString() == "platform";
    if (platformSession && platform && platform->isConnected()) {
        if (approved) platform->approveSession(id); else platform->rejectSession(id);
    }
    appendActivity(id, approved ? "本机用户已批准 Agent 会话" : "本机用户已拒绝 Agent 会话",
                   ActivityKind::Session);
    if (approved && platformSession) syncActivityPanel();
    showStatusMessage(approved ? "会话已批准" : "连接申请已拒绝", 4000);
}


void MainWindow::refreshActiveSessions() {
    while (QLayoutItem *item = activeLayout->takeAt(0)) {
        if (item->widget()) item->widget()->deleteLater();
        delete item;
    }
    bool hasActive = false;
    const QStringList sessionIds = sessions.keys();
    for (const QString &sessionId : sessionIds) {
        expireSessionIfNeeded(sessionId);
        const QJsonObject session = sessions.value(sessionId);
        const QString status = session.value("status").toString();
        if (status != "active" && status != "approved") continue;
        hasActive = true;
        const QString id = sessionId;
        auto *card = new QFrame;
        card->setStyleSheet("QFrame { background: #edf8f2; border: 1px solid #b9e2ca; border-radius: 7px; }");
        auto *column = new QVBoxLayout(card);
        column->setContentsMargins(13, 11, 13, 11);
        auto *titleRow = new QHBoxLayout;
        titleRow->addWidget(makeLabel("活跃会话", "font-size: 14px; font-weight: 650; color: #185c3b;"));
        titleRow->addStretch();
        auto *viewActivity = makeButton("查看活动");
        viewActivity->setObjectName("subtle");
        titleRow->addWidget(viewActivity);
        auto *stop = makeDangerButton(QStringLiteral("立即停止会话"));
        titleRow->addWidget(stop);
        column->addLayout(titleRow);
        const qint64 remainingSeconds = qMax<qint64>(0, (static_cast<qint64>(session.value("expires_at_ms").toDouble()) - QDateTime::currentMSecsSinceEpoch()) / 1000);
        const QString remaining = QString("%1:%2").arg(remainingSeconds / 60, 2, 10, QLatin1Char('0')).arg(remainingSeconds % 60, 2, 10, QLatin1Char('0'));
        auto *details = makeLabel(QString("操作者：%1  ·  来源设备：%2\n开始时间：%3  ·  剩余时间：%4\n任务：%5")
            .arg(session.value("requester").toString(), session.value("source_device").toString(),
                 session.value("decided_at").toString(), remaining, activeTaskSummary(id)),
            "font-size: 12px; color: #536b5e;");
        details->setWordWrap(true);
        column->addWidget(details);
        connect(viewActivity, &QPushButton::clicked, this, [this, id] { showActivityWindow(id); });
        connect(stop, &QPushButton::clicked, this, [this, id] {
            if (askConfirm(this, "立即停止会话", "停止后将撤销授权并取消正在运行的任务。", "立即停止"))
                closeSession(id);
        });
        activeLayout->addWidget(card);
    }
    activePanel->setVisible(hasActive);
    // Session changes are the only thing that may update the activity panel; the
    // panel itself is opened by the owner, never by this refresh.
    syncActivityPanel();
}


QString MainWindow::activeTaskSummary(const QString &sessionId) const {
    for (auto it = tasks.cbegin(); it != tasks.cend(); ++it) {
        if (it.value().value("session_id").toString() == sessionId && it.value().value("status").toString() == "running")
            return "PowerShell 正在运行 (" + it.key().left(8) + ")";
    }
    return "无运行任务";
}


QJsonObject MainWindow::closeSession(const QString &id) {
    if (!sessions.contains(id)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
    QJsonObject session = sessions.value(id);
    const bool platformSession = session.value("source").toString().startsWith(QStringLiteral("platform"));
    session.insert("status", "closed");
    sessions.insert(id, session);
    persistSessionState();
    stopTasksForSession(id, "cancelled");
    refreshPendingRequests();
    refreshActiveSessions();
    refreshSessionsPage();
    notificationSessions.remove(id);
    if (platformSession && platform && platform->isConnected()) platform->closeSession(id);
    appendActivity(id, "本机会话已断开，远程控制已停止", ActivityKind::Session);
    showStatusMessage("会话已关闭", 3000);
    return ok(session);
}


QString MainWindow::sessionStatusText(const QString &sessionId) const {
    const QString status = sessions.value(sessionId).value("status").toString();
    if (status == "active" || status == "approved") return "远程协助进行中";
    if (status == "pending_approval") return "等待本机批准";
    if (status == "closed") return "已断开，远程控制已停止";
    if (status == "expired") return "会话已过期";
    if (status == "rejected") return "请求已拒绝";
    return status.isEmpty() ? "无活动会话" : status;
}
