#include "mainwindow.h"

#include "yiukdialog.h"


void MainWindow::appendActivity(const QString &sessionId, const QString &message) {
    if (sessionId.isEmpty() || message.isEmpty()) return;
    QStringList &lines = activityLogs[sessionId];
    const QString timestamp = QDateTime::currentDateTime().toString("HH:mm:ss");
    for (const QString &line : message.split('\n'))
        lines.append(QString("[%1] %2").arg(timestamp, line));
    while (lines.size() > 500) lines.removeFirst();
    if (activityWindow && activityWindow->isVisible() && activitySessionSelector &&
        activitySessionSelector->currentData().toString() == sessionId) {
        activityLog->setPlainText(lines.join('\n'));
        activityLog->moveCursor(QTextCursor::End);
        activityStatusValue->setText(sessionStatusText(sessionId));
    }
}


void MainWindow::refreshActivityWindow() {
    if (!activityWindow || !activitySessionSelector || !activityLog) return;
    const QString selected = activitySessionSelector->currentData().toString();
    activitySessionSelector->blockSignals(true);
    activitySessionSelector->clear();
    for (auto it = sessions.cbegin(); it != sessions.cend(); ++it) {
        const QJsonObject session = it.value();
        if (session.value("source").toString() != "platform") continue;
        const QString label = QString("%1 · %2 · %3")
            .arg(session.value("purpose").toString("远程协助"),
                 session.value("requester").toString("Agent"),
                 sessionStatusText(it.key()));
        activitySessionSelector->addItem(label, it.key());
    }
    int index = activitySessionSelector->findData(selected);
    if (index < 0 && activitySessionSelector->count() > 0) index = 0;
    if (index >= 0) activitySessionSelector->setCurrentIndex(index);
    activitySessionSelector->blockSignals(false);
    const QString id = activitySessionSelector->currentData().toString();
    activityLog->setPlainText(activityLogs.value(id).join('\n'));
    activityLog->moveCursor(QTextCursor::End);
    const QString status = id.isEmpty() ? QString() : sessions.value(id).value("status").toString();
    activityStatusValue->setText(sessionStatusText(id));
    // The green state belongs to a live session only.
    activityStatusValue->setStyleSheet(status == "active" || status == "approved"
        ? "font-size: 12px; color: #16825d;"
        : "font-size: 12px; color: #687588;");
    activityDisconnectButton->setEnabled(!id.isEmpty() && (status == "active" || status == "approved"));
}


void MainWindow::showActivityWindow(const QString &sessionId) {
    if (!activityWindow) {
        // Same frameless white sheet as the main window: title on the shared
        // title bar, close with the main window's own glyph.
        auto *dialog = new YiukDialog(this, QStringLiteral("远程协助活动"));
        activityWindow = dialog;
        activityWindow->setModal(false);
        activityWindow->setAttribute(Qt::WA_DeleteOnClose, false);
        activityWindow->resize(720, 460);

        auto *sessionRow = new QHBoxLayout;
        sessionRow->setSpacing(10);
        sessionRow->addWidget(makeLabel("当前 Agent", "font-size: 12px; color: #69788d;"));
        activitySessionSelector = new QComboBox;
        // Reuse the settings field look instead of the Fusion default combo.
        activitySessionSelector->setObjectName("settingsField");
        activitySessionSelector->setMinimumHeight(32);
        sessionRow->addWidget(activitySessionSelector, 1);
        activityStatusValue = makeLabel("无活动会话", "font-size: 12px; color: #687588;");
        sessionRow->addWidget(activityStatusValue);
        dialog->content()->addLayout(sessionRow);

        activityLog = new QPlainTextEdit;
        activityLog->setReadOnly(true);
        activityLog->setLineWrapMode(QPlainTextEdit::NoWrap);
        activityLog->setStyleSheet("QPlainTextEdit { background: #ffffff; color: #263446; border: 1px solid #e1e7ef; "
                                   "border-radius: 8px; padding: 10px; font-family: Consolas, monospace; font-size: 12px; }"
                                   "QPlainTextEdit:focus { border-color: #1479ed; }");
        dialog->content()->addWidget(activityLog, 1);

        auto *hint = makeLabel("此窗口仅用于查看活动，关闭窗口不会断开会话。需要停止 Agent 控制，请点击“断开会话”。",
                               "font-size: 12px; color: #69788d;");
        hint->setWordWrap(true);
        dialog->content()->addWidget(hint);

        auto *closeButton = dialog->addButton(QStringLiteral("关闭"));
        activityDisconnectButton = dialog->addButton(QStringLiteral("断开会话"), YiukDialog::ButtonKind::Danger);
        connect(closeButton, &QPushButton::clicked, activityWindow, &QWidget::close);
        connect(activitySessionSelector, qOverload<int>(&QComboBox::currentIndexChanged), this, [this] { refreshActivityWindow(); });
        connect(activityDisconnectButton, &QPushButton::clicked, this, [this] {
            const QString id = activitySessionSelector->currentData().toString();
            if (id.isEmpty()) return;
            if (askConfirm(this, "断开远程协助", "断开后 Agent 将无法继续控制本机，正在运行的任务也会停止。", "断开会话"))
                closeSession(id);
        });
    }
    refreshActivityWindow();
    if (!sessionId.isEmpty()) {
        const int index = activitySessionSelector->findData(sessionId);
        if (index >= 0) activitySessionSelector->setCurrentIndex(index);
    }
    activityWindow->show();
    activityWindow->raise();
    activityWindow->activateWindow();
}
