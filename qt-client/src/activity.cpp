#include "mainwindow.h"


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
    activityStatusValue->setText(sessionStatusText(id));
    activityDisconnectButton->setEnabled(!id.isEmpty() && (sessions.value(id).value("status").toString() == "active" || sessions.value(id).value("status").toString() == "approved"));
}


void MainWindow::showActivityWindow(const QString &sessionId) {
    if (!activityWindow) {
        activityWindow = new QDialog(this);
        activityWindow->setWindowTitle("远程协助活动");
        activityWindow->setModal(false);
        activityWindow->setAttribute(Qt::WA_DeleteOnClose, false);
        activityWindow->resize(720, 460);
        auto *layout = new QVBoxLayout(activityWindow);
        layout->setContentsMargins(16, 16, 16, 16);
        layout->setSpacing(10);
        auto *header = new QHBoxLayout;
        header->addWidget(makeLabel("远程协助活动", "font-size: 17px; font-weight: 650; color: #1d2939;"));
        header->addStretch();
        activityStatusValue = makeLabel("无活动会话", "font-size: 12px; color: #16825d;");
        header->addWidget(activityStatusValue);
        layout->addLayout(header);
        auto *sessionRow = new QHBoxLayout;
        sessionRow->addWidget(makeLabel("当前 Agent", "font-size: 12px; color: #69788d;"));
        activitySessionSelector = new QComboBox;
        sessionRow->addWidget(activitySessionSelector, 1);
        activityDisconnectButton = makeButton("断开会话");
        activityDisconnectButton->setStyleSheet("QPushButton { color: #a52b2b; border: 1px solid #e8bcbc; background: white; padding: 7px 12px; } QPushButton:hover { background: #fff0f0; }");
        sessionRow->addWidget(activityDisconnectButton);
        layout->addLayout(sessionRow);
        activityLog = new QPlainTextEdit;
        activityLog->setReadOnly(true);
        activityLog->setLineWrapMode(QPlainTextEdit::NoWrap);
        activityLog->setStyleSheet("QPlainTextEdit { background: #101820; color: #d5e3ef; border: 1px solid #2e4556; border-radius: 6px; padding: 10px; font-family: Consolas, monospace; font-size: 12px; }");
        layout->addWidget(activityLog, 1);
        auto *hint = makeLabel("此窗口仅用于查看活动，关闭窗口不会断开会话。需要停止 Agent 控制，请点击“断开会话”。", "font-size: 12px; color: #69788d;");
        hint->setWordWrap(true);
        layout->addWidget(hint);
        connect(activitySessionSelector, qOverload<int>(&QComboBox::currentIndexChanged), this, [this] { refreshActivityWindow(); });
        connect(activityDisconnectButton, &QPushButton::clicked, this, [this] {
            const QString id = activitySessionSelector->currentData().toString();
            if (id.isEmpty()) return;
            if (QMessageBox::question(this, "断开远程协助", "断开后 Agent 将无法继续控制本机，正在运行的任务也会停止。") == QMessageBox::Yes)
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
