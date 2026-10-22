#include "mainwindow.h"
#include "titlebar.h"


MainWindow::MainWindow() {
    setWindowTitle(QStringLiteral("Yiuk Device Assist ") + QStringLiteral(YIUK_CLIENT_VERSION));
    setWindowFlags(Qt::Window | Qt::FramelessWindowHint);
    resize(960, 600);
    setMinimumSize(960, 600);
    setStyleSheet(R"(
            QMainWindow, QWidget#root, QDialog, QMessageBox { background: #ffffff; color: #182230; }
            QDialog QLabel, QMessageBox QLabel { color: #182230; background: transparent; }
            QDialog QPushButton, QMessageBox QPushButton { background: #ffffff; color: #263446; border: 1px solid #cfd7e2; border-radius: 6px; padding: 7px 14px; min-width: 72px; }
            QDialog QPushButton:hover, QMessageBox QPushButton:hover { background: #f2f6fb; }
            QLabel { background: transparent; }
            QFrame#sidebar { background: #f1f4f8; border: none; }
            QLabel#muted { color: #687588; }
            QLabel#eyebrow { color: #66758a; font-size: 13px; }
            QLabel#sectionTitle { font-size: 18px; font-weight: 650; }
            QFrame#identityPanel { background: #f5f7fa; border: 1px solid #edf0f4; border-radius: 14px; }
            QFrame#field { background: #ffffff; border: 1px solid #cbd3de; border-radius: 14px; }
            QFrame#settingsCard { background: #ffffff; border: 1px solid #e1e7ef; border-radius: 8px; }
            QFrame#settingsNav { background: #f7f9fc; border: 1px solid #e5eaf1; border-radius: 8px; }
            QLineEdit { border: none; background: transparent; color: #182230; font-size: 17px; selection-background-color: #b9d7ff; }
            QLineEdit#settingsField { border: 1px solid #cbd5e1; border-radius: 6px; background: #ffffff; color: #182230; font-size: 14px; padding: 8px 10px; }
            QLineEdit#settingsField:focus { border-color: #1479ed; }
            QComboBox#settingsField { border: 1px solid #cbd5e1; border-radius: 6px; background: #ffffff; color: #182230; font-size: 14px; padding: 7px 10px; min-height: 20px; }
            QComboBox#settingsField:focus { border-color: #1479ed; }
            QComboBox#settingsField::drop-down { border: none; width: 28px; }
            QScrollArea#settingsScroll { border: none; background: transparent; }
            QPushButton#settingsCategory { text-align: left; border: none; border-radius: 6px; background: transparent; color: #536174; padding: 10px 12px; font-size: 13px; }
            QPushButton#settingsCategory:hover { background: #edf3fa; color: #126fe0; }
            QPushButton#settingsCategory:checked { background: #e2edf9; color: #126fe0; font-weight: 600; }
            QLabel#settingsFieldLabel { color: #5f6d80; font-size: 13px; }
            QPushButton { border: 1px solid #d6dde7; border-radius: 6px; background: #ffffff; padding: 8px 12px; color: #334155; }
            QPushButton:hover { background: #f3f7fc; border-color: #b9c7d8; }
            QPushButton#primary { background: #1479ed; border: 1px solid #1479ed; color: #ffffff; font-size: 15px; font-weight: 600; padding: 12px 24px; }
            QPushButton#primary:hover { background: #096bd7; }
            QPushButton#navItem { text-align: left; border: none; background: transparent; padding: 10px 12px; color: #536174; font-size: 14px; }
            QPushButton#navItem:checked { background: #e2e9f2; color: #126fe0; font-weight: 600; }
            QPushButton#subtle { border: none; padding: 5px 8px; color: #627086; }
            QPushButton#passwordEye,
            QPushButton#passwordEye:hover,
            QPushButton#passwordEye:pressed,
            QPushButton#passwordEye:checked,
            QPushButton#passwordEye:checked:hover,
            QPushButton#passwordEye:focus {
                border: 0;
                outline: 0;
                padding: 0;
                background: transparent;
            }
            QPushButton#passwordEye:hover { background: #e8edf3; border-radius: 5px; }
            QPushButton#passwordEye:pressed { background: #dce4ed; border-radius: 5px; }
            QPushButton#passwordEye:checked { background: #e8f1fd; border-radius: 5px; }
            QPushButton#passwordEye:checked:hover { background: #dceafd; }
            QPushButton#passwordEye::menu-indicator { image: none; }
            QPushButton#toolIcon { border: 0; background: transparent; padding: 0; border-radius: 5px; }
            QPushButton#toolIcon:hover { background: #e9eef5; }
            QPushButton#toolIcon:pressed { background: #dce4ed; }
            QCheckBox { color: #5e6b7d; spacing: 8px; }
            QCheckBox::indicator { width: 16px; height: 16px; }
            QCheckBox::indicator:unchecked { border: 1px solid #bdc7d4; border-radius: 3px; background: white; }
            QCheckBox::indicator:checked { border: 1px solid #1977eb; border-radius: 3px; background: #1977eb; }
            QRadioButton { color: #536174; spacing: 8px; }
            QRadioButton::indicator { width: 15px; height: 15px; }
            QRadioButton::indicator:unchecked { border: 1px solid #b7c1ce; border-radius: 8px; background: white; }
            QRadioButton::indicator:checked { border: 4px solid #1977eb; border-radius: 8px; background: white; }
        )");

    auto *root = new QWidget;
    root->setObjectName("root");
    auto *layout = new QVBoxLayout(root);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(0);
    layout->addWidget(new TitleBar(root));
    auto *body = new QWidget;
    auto *bodyLayout = new QHBoxLayout(body);
    bodyLayout->setContentsMargins(0, 0, 0, 0);
    bodyLayout->setSpacing(0);
    bodyLayout->addWidget(createSidebar());
    savedDevices = QSettings().value("savedDevices").toStringList();
    loadSessionState();
    pages = new QStackedWidget;
    pages->addWidget(createHomePage());
    pages->addWidget(createDevicesPage());
    pages->addWidget(createSessionsPage());
    pages->addWidget(createSettingsPage());
    bodyLayout->addWidget(pages, 1);
    layout->addWidget(body, 1);
    setCentralWidget(root);
    setupSystemTray();
    startLocalBridge();
    startPlatform();
    QTimer::singleShot(0, this, [this] {
        if (acceptingRequests && acceptingRequests->isChecked() && !QSettings().value("localDemoConsent", false).toBool())
            confirmLocalDemoConsent();
    });
}


void MainWindow::setupSystemTray() {
    trayIcon = new QSystemTrayIcon(QApplication::style()->standardIcon(QStyle::SP_ComputerIcon), this);
    trayIcon->setToolTip("Yiuk Device Assist");
    auto *menu = new QMenu(this);
    auto *open = menu->addAction("打开 Yiuk Device Assist");
    auto *activity = menu->addAction("打开远程协助活动");
    auto *quit = menu->addAction("退出");
    connect(open, &QAction::triggered, this, [this] { showNormal(); activateWindow(); });
    connect(activity, &QAction::triggered, this, [this] { showActivityWindow(); });
    connect(quit, &QAction::triggered, qApp, &QApplication::quit);
    trayIcon->setContextMenu(menu);
    connect(trayIcon, &QSystemTrayIcon::messageClicked, this, [this] {
        if (notificationSessions.isEmpty()) {
            showNormal();
            activateWindow();
            return;
        }
        const QString sessionId = notificationSessions.keys().first();
        notificationSessions.remove(sessionId);
        showApprovalDialog(sessionId);
    });
    trayIcon->show();
}


QString MainWindow::loadOrCreateDeviceCode() {
    const QString configRoot = yiukConfigRoot();
    QDir().mkpath(configRoot);
    const QString path = findYiukConfigPath("device.json");
    QFile file(path);
    if (file.open(QIODevice::ReadOnly)) {
        const QJsonDocument doc = QJsonDocument::fromJson(file.readAll());
        const QString id = doc.object().value("device_id").toString();
        if (id.size() == 9) return spacedCode(id);
    }
    const QString id = randomDigits(9);
    if (file.open(QIODevice::WriteOnly | QIODevice::Truncate))
        file.write(QJsonDocument(QJsonObject{{"device_id", id}}).toJson(QJsonDocument::Compact));
    return spacedCode(id);
}


QWidget *MainWindow::createSidebar() {
    auto *sidebar = new QFrame;
    sidebar->setObjectName("sidebar");
    sidebar->setFixedWidth(212);
    auto *layout = new QVBoxLayout(sidebar);
    layout->setContentsMargins(15, 20, 15, 16);
    layout->setSpacing(7);

    layout->addWidget(makeLabel("YIUK", "font-size: 17px; font-weight: 800; color: #1677ed; padding: 0 4px;"));
    layout->addWidget(makeLabel("DEVICE ASSIST", "font-size: 10px; color: #8995a5; padding: 0 5px;"));
    layout->addSpacing(22);
    auto *avatar = makeLabel("Y", "background: #2378ed; color: white; font-size: 20px; font-weight: 700; border-radius: 25px;");
    avatar->setAlignment(Qt::AlignCenter);
    avatar->setFixedSize(50, 50);
    layout->addWidget(avatar, 0, Qt::AlignHCenter);
    layout->addWidget(makeLabel("本机设备", "font-size: 14px; font-weight: 600;"), 0, Qt::AlignHCenter);
    layout->addWidget(makeLabel("Windows · 当前用户", "font-size: 11px; color: #8490a0;"), 0, Qt::AlignHCenter);
    layout->addSpacing(18);

    navHome = navButton("主页", Glyph::Home, true);
    navDevices = navButton("我的设备", Glyph::Devices);
    navHistory = navButton("会话记录", Glyph::History);
    navSettings = navButton("设置", Glyph::Settings);
    layout->addWidget(navHome);
    layout->addWidget(navDevices);
    layout->addWidget(navHistory);
    layout->addWidget(navSettings);
    connect(navHome, &QPushButton::clicked, this, [this] { showPage(0); });
    connect(navDevices, &QPushButton::clicked, this, [this] { refreshDevicesPage(); showPage(1); });
    connect(navHistory, &QPushButton::clicked, this, [this] { refreshSessionsPage(); showPage(2); });
    connect(navSettings, &QPushButton::clicked, this, [this] { refreshSettingsPage(); showPage(3); });
    layout->addSpacing(14);
    layout->addWidget(makeLabel("连接状态", "font-size: 12px; font-weight: 600; color: #69788d; padding: 8px 5px 2px;"));
    mcpState = makeLabel("MCP：启动中", "font-size: 11px; color: #687588; padding: 2px 5px;");
    platformState = makeLabel("平台：未连接", "font-size: 11px; color: #687588; padding: 2px 5px;");
    deviceState = makeLabel("设备：未注册", "font-size: 11px; color: #687588; padding: 2px 5px;");
    layout->addWidget(mcpState);
    layout->addWidget(platformState);
    layout->addWidget(deviceState);
    serviceState = mcpState;
    layout->addStretch();
    return sidebar;
}


QPushButton *MainWindow::navButton(const QString &text, Glyph glyph, bool active) {
    auto *button = new QPushButton(text);
    button->setObjectName("navItem");
    button->setIcon(glyphIcon(glyph));
    button->setIconSize(QSize(18, 18));
    button->setLayoutDirection(Qt::LeftToRight);
    button->setCursor(Qt::PointingHandCursor);
    button->setCheckable(true);
    button->setChecked(active);
    button->setMinimumHeight(42);
    return button;
}


QPushButton *MainWindow::iconButton(Glyph glyph, const QString &tooltip) {
    auto *button = new QPushButton;
    button->setObjectName("toolIcon");
    button->setIcon(glyphIcon(glyph));
    button->setIconSize(QSize(18, 18));
    button->setFixedSize(30, 30);
    button->setToolTip(tooltip);
    button->setAccessibleName(tooltip);
    button->setCursor(Qt::PointingHandCursor);
    return button;
}


QString MainWindow::sessionStatePath() const {
    return yiukConfigPath("sessions.json");
}


void MainWindow::loadSessionState() {
    QFile file(sessionStatePath());
    if (!file.open(QIODevice::ReadOnly)) return;
    const QJsonDocument document = QJsonDocument::fromJson(file.readAll());
    if (!document.isObject()) return;
    const QJsonObject root = document.object();
    for (const QJsonValue &value : root.value("sessions").toArray()) {
        if (!value.isObject()) continue;
        QJsonObject session = value.toObject();
        const QString status = session.value("status").toString();
        if (status == "active" || status == "pending_approval") {
            session.insert("status", "interrupted");
            session.insert("end_reason", "客户端退出或重启");
        }
        const QString id = session.value("session_id").toString();
        if (!id.isEmpty()) sessions.insert(id, session);
    }
    for (const QJsonValue &value : root.value("tasks").toArray()) {
        if (!value.isObject()) continue;
        QJsonObject task = value.toObject();
        if (task.value("status").toString() == "running") task.insert("status", "result_unknown");
        const QString id = task.value("request_id").toString();
        if (!id.isEmpty()) tasks.insert(id, task);
    }
    persistSessionState();
}


void MainWindow::persistSessionState() {
    const QString path = sessionStatePath();
    QDir().mkpath(QFileInfo(path).absolutePath());
    QJsonArray sessionArray;
    const QStringList sessionIds = sessions.keys();
    for (const QString &id : sessionIds) sessionArray.append(sessions.value(id));
    while (sessionArray.size() > 100) sessionArray.removeAt(0);
    QJsonArray taskArray;
    const QStringList taskIds = tasks.keys();
    for (const QString &id : taskIds) taskArray.append(tasks.value(id));
    while (taskArray.size() > 300) taskArray.removeAt(0);
    QFile file(path);
    if (file.open(QIODevice::WriteOnly | QIODevice::Truncate))
        file.write(QJsonDocument(QJsonObject{{"sessions", sessionArray}, {"tasks", taskArray}}).toJson(QJsonDocument::Compact));
}


bool MainWindow::confirmLocalDemoConsent() {
    if (QSettings().value("localDemoConsent", false).toBool()) return true;
    QMessageBox warning(this);
    warning.setWindowTitle("协助权限确认");
    warning.setIcon(QMessageBox::Warning);
    warning.setText("允许本机接收协助请求？");
    warning.setInformativeText("已配置的 MCP 进程将以当前 Windows 用户权限执行 PowerShell。这不是沙箱，请仅对可信请求启用。");
    warning.setStandardButtons(QMessageBox::Yes | QMessageBox::Cancel);
    warning.setDefaultButton(QMessageBox::Cancel);
    if (warning.exec() != QMessageBox::Yes) return false;
    QSettings().setValue("localDemoConsent", true);
    return true;
}


QWidget *MainWindow::pageShell(const QString &title, const QString &subtitle, QVBoxLayout **outLayout) {
    auto *page = new QWidget;
    auto *layout = new QVBoxLayout(page);
    layout->setContentsMargins(57, 30, 57, 26);
    layout->setSpacing(0);
    layout->addWidget(makeLabel(title, "font-size: 22px; font-weight: 650; color: #1d2939;"));
    layout->addSpacing(5);
    auto *description = makeLabel(subtitle, "font-size: 13px; color: #7a8798;");
    description->setWordWrap(true);
    layout->addWidget(description);
    layout->addSpacing(22);
    *outLayout = layout;
    return page;
}


QWidget *MainWindow::deviceRow(const QString &code, const QString &name, bool local) {
    auto *row = new QFrame;
    row->setObjectName("identityPanel");
    auto *layout = new QHBoxLayout(row);
    layout->setContentsMargins(16, 12, 16, 12);
    layout->setSpacing(14);
    auto *state = makeLabel(local ? "● 在线" : "● 未连接", local ? "font-size: 12px; color: #16825d;" : "font-size: 12px; color: #687588;");
    state->setFixedWidth(65);
    layout->addWidget(state);
    auto *details = new QVBoxLayout;
    details->setSpacing(3);
    details->addWidget(makeLabel(name, "font-size: 14px; font-weight: 650; color: #263446;"));
    details->addWidget(makeLabel(code + (local ? "  ·  Windows · 当前用户" : "  ·  等待平台连接"), "font-size: 12px; color: #687588;"));
    layout->addLayout(details, 1);
    auto *action = makeButton(local ? "复制" : "发起协助");
    action->setToolTip(local ? "复制设备码" : "发起协助");
    action->setEnabled(local || platformState->text() == "平台：已连接");
    layout->addWidget(action);
    if (local) {
        connect(action, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
    } else {
        connect(action, &QPushButton::clicked, this, [this, code] {
            remoteCode->setText(code);
            showPage(0);
            remoteCode->setFocus();
        });
    }
    return row;
}


void MainWindow::showPage(int index) {
    pages->setCurrentIndex(index);
    navHome->setChecked(index == 0);
    navDevices->setChecked(index == 1);
    navHistory->setChecked(index == 2);
    navSettings->setChecked(index == 3);
}


void MainWindow::refreshDevicesPage() {
    if (deviceIdValue) deviceIdValue->setText(deviceCode);
    if (deviceStatusValue)
        deviceStatusValue->setText(platform && platform->isConnected() ? "平台已连接 · 设备已注册" : "平台未连接");
    savedDevices = QSettings().value("savedDevices").toStringList();
    if (!deviceListLayout) return;
    while (QLayoutItem *item = deviceListLayout->takeAt(0)) {
        if (item->widget()) item->widget()->deleteLater();
        delete item;
    }
    deviceListLayout->addWidget(deviceRow(deviceCode, "本机设备", true));
    for (const QString &code : savedDevices)
        deviceListLayout->addWidget(deviceRow(code, "已添加设备", false));

}


void MainWindow::refreshSessionsPage() {
    if (!sessionsHistory) return;
    QStringList lines;
    const QStringList ids = sessions.keys();
    for (const QString &id : ids) {
        const QJsonObject session = sessions.value(id);
        lines.append(QString("%1  |  %2  |  %3\n请求者：%4\n用途：%5\n权限：%6")
            .arg(session.value("created_at").toString(), session.value("status").toString(), id.left(8),
                 session.value("requester").toString(), session.value("purpose").toString(),
                 jsonArrayText(session.value("permissions").toArray())));
        for (auto task = tasks.cbegin(); task != tasks.cend(); ++task) {
            if (task.value().value("session_id").toString() == id)
                lines.append(QString("  任务 %1  %2  退出码：%3")
                    .arg(task.key().left(8), task.value().value("status").toString(),
                         task.value().value("exit_code").isNull() ? "未返回" : QString::number(task.value().value("exit_code").toInt())));
        }
        lines.append(QString());
    }
    sessionsHistory->setText(lines.isEmpty() ? "暂无会话记录。" : lines.join("\n"));
}


void MainWindow::refreshSettingsPage() {
    if (settingsStatusValue && acceptingRequests)
        settingsStatusValue->setText(acceptingRequests->isChecked() ? "状态：协助请求已启用" : "状态：协助请求已暂停");
}


QString MainWindow::deviceId() const {
    QString id = deviceCode;
    id.remove(' ');
    return id;
}


void MainWindow::copyToClipboard(const QString &text, const QString &message) {
    QApplication::clipboard()->setText(text);
    statusBar()->showMessage(message, 1800);
}
