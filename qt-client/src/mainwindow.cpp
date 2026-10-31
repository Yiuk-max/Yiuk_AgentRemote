#include "mainwindow.h"
#include "titlebar.h"

#include <QCursor>
#include <QEvent>
#include <QScrollBar>

#include <algorithm>


MainWindow::MainWindow() {
    setWindowTitle(QStringLiteral("Yiuk Device Assist ") + QStringLiteral(YIUK_CLIENT_VERSION));
    setWindowFlags(Qt::Window | Qt::FramelessWindowHint);
    resize(960, 600);
    setMinimumSize(960, 600);
    setStyleSheet(R"(
            /* 主窗口和所有弹窗共用同一白色底板
               弹窗采用无边框样式并复用此窗口自绘的标题栏，不会回退到 Windows 原生边框
               对话框窗口本身保持透明，由 #dialogCard 绘制白色表面和细边框，为投影留出空间 */
            QMainWindow, QWidget#root { background: #ffffff; color: #182230; }
            QDialog { background: transparent; color: #182230; }
            QWidget#dialogCard { background: #ffffff; border: 1px solid #d5dde8; }
            QDialog QLabel { color: #182230; background: transparent; }
            QDialog QPushButton { background: #ffffff; color: #263446; border: 1px solid #cfd7e2; border-radius: 6px; padding: 7px 14px; min-width: 72px; }
            QDialog QPushButton:hover { background: #f2f6fb; }
            QDialog QLineEdit#settingsField, QDialog QComboBox#settingsField { min-height: 20px; }
            QLabel { background: transparent; }
            QFrame#sidebar { background: #f1f4f8; border: none; }
            QLabel#muted { color: #687588; }
            QLabel#eyebrow { color: #66758a; font-size: 13px; }
            QLabel#sectionTitle { font-size: 18px; font-weight: 650; }
            QFrame#identityPanel { background: #f5f7fa; border: 1px solid #edf0f4; border-radius: 14px; }
            QFrame#field { background: #ffffff; border: 1px solid #cbd3de; border-radius: 14px; }
            QFrame#settingsCard { background: #ffffff; border: 1px solid #e1e7ef; border-radius: 8px; }
            QLineEdit { border: none; background: transparent; color: #182230; font-size: 17px; selection-background-color: #b9d7ff; }
            QLineEdit#settingsField { border: 1px solid #cbd5e1; border-radius: 6px; background: #ffffff; color: #182230; font-size: 14px; padding: 8px 10px; }
            QLineEdit#settingsField:focus { border-color: #1479ed; }
            QComboBox#settingsField { border: 1px solid #cbd5e1; border-radius: 6px; background: #ffffff; color: #182230; font-size: 14px; padding: 7px 10px; min-height: 20px; }
            QComboBox#settingsField:focus { border-color: #1479ed; }
            QComboBox#settingsField::drop-down { border: none; width: 28px; }
            QScrollArea#settingsScroll { border: none; background: transparent; }
            QScrollArea#sessionsScroll { border: none; background: transparent; }
            QFrame#sessionRow { background: #f7f9fc; border: 1px solid #e4eaf2; border-radius: 8px; }
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
            QCheckBox::indicator:checked { border: 1px solid #1977eb; border-radius: 3px; background: #1977eb url(:/icons/check.svg) center center no-repeat; }
            QRadioButton { color: #536174; spacing: 8px; }
            QRadioButton::indicator { width: 15px; height: 15px; }
            QRadioButton::indicator:unchecked { border: 1px solid #b7c1ce; border-radius: 8px; background: white; }
            QRadioButton::indicator:checked { border: 4px solid #1977eb; border-radius: 8px; background: white; }
            /* 弹出界面（托盘菜单、行上下文菜单、下拉列表）与页面使用相同的白色底板和蓝色高亮
               工具提示是顶层弹窗，因此其样式由 main.cpp 中的 QApplication 设置 */
            QMenu { background: #ffffff; border: 1px solid #e1e7ef; border-radius: 8px; padding: 4px; }
            QMenu::item { padding: 7px 20px; border-radius: 6px; color: #263446; font-size: 13px; }
            QMenu::item:selected { background: #e8f1fd; color: #126fe0; }
            QMenu::separator { height: 1px; background: #eef1f5; margin: 4px 8px; }
            QComboBox QAbstractItemView { background: #ffffff; border: 1px solid #e1e7ef; border-radius: 8px; padding: 4px; outline: 0; selection-background-color: #e8f1fd; selection-color: #126fe0; }
        )");

    auto *root = new QWidget;
    root->setObjectName("root");
    auto *layout = new QHBoxLayout(root);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(0);
    layout->addWidget(createSidebar());

    auto *content = new QWidget;
    auto *contentLayout = new QVBoxLayout(content);
    contentLayout->setContentsMargins(0, 0, 0, 0);
    contentLayout->setSpacing(0);
    contentLayout->addWidget(new TitleBar(content));
    savedDevices = QSettings().value("savedDevices").toStringList();
    loadSessionState();
    pages = new QStackedWidget;
    pages->addWidget(createHomePage());
    pages->addWidget(createDevicesPage());
    pages->addWidget(createSessionsPage());
    pages->addWidget(createSettingsPage());
    contentLayout->addWidget(pages, 1);
    // 状态栏应位于右侧内容列内
    // QMainWindow 状态栏会横跨整个窗口，导致侧边栏在状态栏上方结束
    statusFooter = new QStatusBar(content);
    statusFooter->setSizeGripEnabled(false);
    statusFooter->setFixedHeight(28);
    statusFooter->setStyleSheet("QStatusBar { background: #ffffff; border-top: 1px solid #e5eaf1; color: #69788d; font-size: 12px; padding-left: 12px; } QStatusBar::item { border: none; }");
    contentLayout->addWidget(statusFooter);
    layout->addWidget(content, 1);
    setCentralWidget(root);
    setupSystemTray();
    startLocalBridge();
    startPlatform();
}


void MainWindow::setupSystemTray() {
    trayIcon = new QSystemTrayIcon(QApplication::style()->standardIcon(QStyle::SP_ComputerIcon), this);
    trayIcon->setToolTip("Yiuk Device Assist");
    auto *menu = new QMenu(this);
    auto *open = menu->addAction("打开 Yiuk Device Assist");
    auto *activity = menu->addAction("打开远程协助活动");
    auto *quit = menu->addAction("退出");
    connect(open, &QAction::triggered, this, [this] { showMainWindow(); });
    connect(activity, &QAction::triggered, this, [this] { showActivityWindow(); });
    connect(quit, &QAction::triggered, this, [this] {
        if (trayIcon) trayIcon->hide();
        qApp->quit();
    });
    trayIcon->setContextMenu(menu);
    connect(trayIcon, &QSystemTrayIcon::activated, this, [this](QSystemTrayIcon::ActivationReason reason) {
        if (reason == QSystemTrayIcon::Trigger || reason == QSystemTrayIcon::DoubleClick)
            showMainWindow();
    });
    connect(trayIcon, &QSystemTrayIcon::messageClicked, this, [this] {
        if (notificationSessions.isEmpty()) {
            showMainWindow();
            return;
        }
        const QString sessionId = notificationSessions.keys().first();
        notificationSessions.remove(sessionId);
        showApprovalDialog(sessionId);
    });
    trayIcon->show();
}


void MainWindow::showMainWindow() {
    showNormal();
    raise();
    activateWindow();
}


void MainWindow::closeEvent(QCloseEvent *event) {
    // Closing the frameless window keeps the resident client alive; use the tray
    // menu's Quit action to terminate the client and its local bridge.
    if (trayIcon && trayIcon->isVisible()) {
        hide();
        event->ignore();
        return;
    }
    QMainWindow::closeEvent(event);
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
    connect(navSettings, &QPushButton::clicked, this, [this] { showPage(3); });
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
        if (status == "active" || status == "approved" || status == "pending_approval" || status == "requested") {
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


QWidget *MainWindow::pageShell(const QString &title, const QString &subtitle, QVBoxLayout **outLayout) {
    auto *page = new QWidget;
    auto *layout = new QVBoxLayout(page);
    layout->setContentsMargins(57, 30, 57, 26);
    layout->setSpacing(0);
    layout->addWidget(makeLabel(title, "font-size: 22px; font-weight: 650; color: #1d2939;"));
    layout->addSpacing(4);
    auto *description = makeLabel(subtitle, "font-size: 13px; color: #7a8798;");
    description->setWordWrap(true);
    layout->addWidget(description);
    layout->addSpacing(14);
    *outLayout = layout;
    return page;
}


QWidget *MainWindow::deviceRow(const QString &code, const QString &name, bool local) {
    auto *row = new QFrame;
    row->setObjectName("identityPanel");
    row->setFixedHeight(72);
    auto *layout = new QHBoxLayout(row);
    layout->setContentsMargins(16, 8, 16, 8);
    layout->setSpacing(14);
    auto *state = makeLabel(local ? "● 在线" : "● 未连接", local ? "font-size: 12px; color: #16825d;" : "font-size: 12px; color: #687588;");
    state->setFixedWidth(65);
    layout->addWidget(state);
    auto *details = new QVBoxLayout;
    details->setSpacing(3);
    auto *nameLabel = makeLabel(name, "font-size: 14px; font-weight: 650; color: #263446;");
    auto *codeLabel = makeLabel(code + (local ? "  ·  Windows · 当前用户" : "  ·  未建立会话"), "font-size: 12px; color: #687588;");
    details->addWidget(nameLabel);
    details->addWidget(codeLabel);
    layout->addLayout(details, 1);
    auto *action = makeButton(local ? "复制" : "发起协助");
    action->setToolTip(local ? "复制设备码" : "发起协助");
    layout->addWidget(action);
    if (local) {
        connect(action, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
    } else {
        // 始终允许点击
        // 之前根据侧边栏状态标签启用该行，但平台连接前创建的行会保留过期状态，导致“发起协助”一直处于灰显
        // 查找设备时已经会告知用户平台是否离线
        auto startAssist = [this, code] {
            QString digits;
            for (const QChar ch : code) if (ch.isDigit()) digits += ch;
            remoteCode->setText(code);
            showPage(0);
            remoteCode->setFocus();
            lookupDeviceBeforeConnecting(digits);
        };
        connect(action, &QPushButton::clicked, this, startAssist);
        auto showContextMenu = [this, row, code, startAssist] {
            QMenu menu(row);
            auto *connectAction = menu.addAction("发起连接");
            auto *deleteAction = menu.addAction("删除设备");
            QAction *chosen = menu.exec(QCursor::pos());
            if (chosen == connectAction) {
                startAssist();
            } else if (chosen == deleteAction) {
                savedDevices.removeAll(code);
                QSettings().setValue("savedDevices", savedDevices);
                refreshDevicesPage();
                refreshFavoriteState();
                showStatusMessage("设备已从列表删除", 1800);
            }
        };
        auto attachContextMenu = [this, showContextMenu](QWidget *widget) {
            widget->setContextMenuPolicy(Qt::CustomContextMenu);
            connect(widget, &QWidget::customContextMenuRequested, this,
                    [showContextMenu](const QPoint &) { showContextMenu(); });
        };
        attachContextMenu(row);
        attachContextMenu(state);
        attachContextMenu(nameLabel);
        attachContextMenu(codeLabel);
        attachContextMenu(action);
    }
    return row;
}


void MainWindow::showPage(int index) {
    pages->setCurrentIndex(index);
    navHome->setChecked(index == 0);
    navDevices->setChecked(index == 1);
    navHistory->setChecked(index == 2);
    navSettings->setChecked(index == 3);
    // 收藏列表可能在其他页面发生变化（添加或删除），因此每次返回主页时都重新检查收藏星标
    if (index == 0) refreshFavoriteState();
    if (index == 3 && settingsPage) {
        // 页面隐藏时加载文本字段可能导致设置页自动滚动；始终从页面标题位置显示
        QTimer::singleShot(0, this, [this] {
            if (auto *scroll = settingsPage->findChild<QScrollArea *>("settingsScroll"))
                scroll->verticalScrollBar()->setValue(0);
        });
    }
}


void MainWindow::refreshDevicesPage() {
    if (deviceIdValue) deviceIdValue->setText(deviceCode);
    savedDevices = QSettings().value("savedDevices").toStringList();
    if (!deviceListLayout) return;
    while (QLayoutItem *item = deviceListLayout->takeAt(0)) {
        if (item->widget()) item->widget()->deleteLater();
        delete item;
    }
    deviceListLayout->addWidget(deviceRow(deviceCode, "本机设备", true));
    for (const QString &code : savedDevices)
        deviceListLayout->addWidget(deviceRow(code, "已添加设备", false));
    deviceListLayout->addStretch(1);
    refreshFavoriteState();
}


namespace {

QString sessionCodeText(const QString &value) {
    QString digits;
    for (const QChar ch : value) if (ch.isDigit()) digits += ch;
    return digits.size() == 9 ? spacedCode(digits) : value;
}

qint64 sessionCreatedMs(const QJsonObject &session) {
    const QJsonValue millis = session.value("created_at_ms");
    if (millis.isDouble()) return static_cast<qint64>(millis.toDouble());
    const QJsonValue created = session.value("created_at");
    if (created.isDouble()) return static_cast<qint64>(created.toDouble() * 1000.0);
    const QDateTime parsed = QDateTime::fromString(created.toString(), Qt::ISODate);
    return parsed.isValid() ? parsed.toMSecsSinceEpoch() : 0;
}

QString sessionTimeText(const QJsonValue &value) {
    if (value.isDouble()) {
        return QDateTime::fromSecsSinceEpoch(static_cast<qint64>(value.toDouble())).toLocalTime()
            .toString("yyyy-MM-dd HH:mm:ss");
    }
    const QDateTime parsed = QDateTime::fromString(value.toString(), Qt::ISODate);
    return parsed.isValid() ? parsed.toLocalTime().toString("yyyy-MM-dd HH:mm:ss") : value.toString();
}

}  // namespace


QString MainWindow::sessionConnectorCode(const QJsonObject &session) const {
    // 如果会话由此客户端发起，连接方就是本设备
    if (session.value("device_id").toString() != deviceId()) return spacedCode(deviceId());
    // 本地 MCP 请求也来自本设备
    if (session.value("source").toString() != QLatin1String("platform")) return spacedCode(deviceId());
    // 旧版会话记录中可能没有远程平台操作端的设备代码
    const QString forwarded = session.value("operator_device_id").toString().trimmed();
    if (!forwarded.isEmpty()) return sessionCodeText(forwarded);
    const QString sourceDevice = session.value("source_device").toString().trimmed();
    if (!sourceDevice.isEmpty()) return sessionCodeText(sourceDevice);
    const QString operatorAddress = session.value("operator_device").toString().trimmed();
    if (!operatorAddress.isEmpty()) return sessionCodeText(operatorAddress);
    return QString();
}


QString MainWindow::sessionTargetCode(const QJsonObject &session) const {
    const QString code = sessionCodeText(session.value("device_id").toString());
    return code.isEmpty() ? QStringLiteral("未提供设备码") : code;
}


void MainWindow::refreshSessionsPage() {
    if (!sessionsListLayout) return;
    while (QLayoutItem *item = sessionsListLayout->takeAt(0)) {
        if (item->widget()) item->widget()->deleteLater();
        delete item;
    }
    QStringList ids = sessions.keys();
    std::sort(ids.begin(), ids.end(), [this](const QString &left, const QString &right) {
        return sessionCreatedMs(sessions.value(left)) > sessionCreatedMs(sessions.value(right));
    });
    if (ids.isEmpty()) {
        sessionsListLayout->addWidget(makeLabel("暂无会话记录。", "color: #69788d; font-size: 13px; padding: 10px 2px;"));
        sessionsListLayout->addStretch(1);
        return;
    }
    for (const QString &id : ids) {
        const QJsonObject session = sessions.value(id);
        const QString connector = sessionConnectorCode(session);
        const QString target = sessionTargetCode(session);

        auto *row = new QFrame;
        row->setObjectName("sessionRow");
        row->setFixedHeight(56);
        auto *layout = new QVBoxLayout(row);
        layout->setContentsMargins(14, 7, 14, 7);
        layout->setSpacing(3);
        auto *codes = makeLabel(QString("连接者 %1   →   被连接者 %2")
            .arg(connector.isEmpty() ? "未提供设备码" : connector, target),
            "font-size: 14px; font-weight: 650; color: #263446;");
        codes->setTextInteractionFlags(Qt::TextSelectableByMouse | Qt::TextSelectableByKeyboard);
        codes->setCursor(Qt::IBeamCursor);
        layout->addWidget(codes);
        auto *time = makeLabel(sessionTimeText(session.value("created_at")),
            "font-size: 12px; color: #69788d;");
        time->setTextInteractionFlags(Qt::TextSelectableByMouse | Qt::TextSelectableByKeyboard);
        time->setCursor(Qt::IBeamCursor);
        layout->addWidget(time);
        sessionsListLayout->addWidget(row);
    }
    sessionsListLayout->addStretch(1);
}


QString MainWindow::deviceId() const {
    QString id = deviceCode;
    id.remove(' ');
    return id;
}


void MainWindow::copyToClipboard(const QString &text, const QString &message) {
    QApplication::clipboard()->setText(text);
    showStatusMessage(message, 1800);
}


void MainWindow::showStatusMessage(const QString &message, int timeout) {
    if (statusFooter) statusFooter->showMessage(message, timeout);
}


bool MainWindow::hasOtherLiveSession(const QString &exceptId) const {
    for (auto it = sessions.cbegin(); it != sessions.cend(); ++it) {
        if (it.key() == exceptId) continue;
        const QString status = it.value().value("status").toString();
        if (status == QLatin1String("requested") || status == QLatin1String("pending_approval") ||
            status == QLatin1String("approved") || status == QLatin1String("active"))
            return true;
    }
    return false;
}


bool MainWindow::acceptPlatformSession(const QJsonObject &session) const {
    const QString id = session.value("session_id").toString();
    if (id.isEmpty() || sessions.contains(id) || session.value("device_id").toString() != deviceId()) return true;
    const QString status = session.value("status").toString();
    if (status != QLatin1String("requested") && status != QLatin1String("pending_approval") &&
        status != QLatin1String("approved") && status != QLatin1String("active"))
        return true;
    return !hasOtherLiveSession();
}


bool MainWindow::eventFilter(QObject *watched, QEvent *event) {
    // QComboBox 默认会在滚动滚轮时切换选项
    // 只有用户主动聚焦授权模式选择框时才处理滚轮输入
    if (watched == platformMode && event->type() == QEvent::Wheel && !platformMode->hasFocus())
        return true;
    return QMainWindow::eventFilter(watched, event);
}
