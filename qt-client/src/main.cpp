#include "localbridge.h"

#include <QApplication>
#include <QCheckBox>
#include <QClipboard>
#include <QFrame>
#include <QHash>
#include <QHBoxLayout>
#include <QLabel>
#include <QIcon>
#include <QLineEdit>
#include <QMainWindow>
#include <QMouseEvent>
#include <QMessageBox>
#include <QProcess>
#include <QStandardPaths>
#include <QTemporaryFile>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QUuid>
#include <QPushButton>
#include <QRandomGenerator>
#include <QStatusBar>
#include <QStyle>
#include <QSystemTrayIcon>
#include <QMenu>
#include <QVBoxLayout>
#include <QWidget>
#include <QWindow>
#include <QDir>
#include <QFile>
#include <QDateTime>
#include <QScrollArea>
#include <QTimer>

namespace {

QLabel *makeLabel(const QString &text, const QString &style = {}) {
    auto *label = new QLabel(text);
    label->setStyleSheet(style);
    return label;
}

QPushButton *makeButton(const QString &text, const QString &style = {}) {
    auto *button = new QPushButton(text);
    button->setCursor(Qt::PointingHandCursor);
    button->setStyleSheet(style);
    return button;
}

QString spacedCode(const QString &digits) {
    if (digits.size() != 9) return digits;
    return digits.left(3) + " " + digits.mid(3, 3) + " " + digits.right(3);
}

QString randomDigits(int count) {
    QString value;
    value.reserve(count);
    for (int i = 0; i < count; ++i)
        value += QString::number(QRandomGenerator::global()->bounded(10));
    return value;
}

class TitleBar final : public QWidget {
public:
    explicit TitleBar(QWidget *parent = nullptr) : QWidget(parent) {
        setFixedHeight(42);
        setStyleSheet("background: #ffffff;");
        auto *layout = new QHBoxLayout(this);
        layout->setContentsMargins(20, 0, 6, 0);
        layout->setSpacing(2);
        layout->addWidget(makeLabel("YIUK", "font-size: 15px; font-weight: 800; color: #1479ed;"));
        layout->addSpacing(10);
        layout->addWidget(makeLabel("设备协助", "font-size: 13px; font-weight: 600; color: #435168;"));
        layout->addStretch();
        auto *minimize = windowButton("−", "minimize");
        auto *maximize = windowButton("□", "maximize");
        auto *close = windowButton("×", "close");
        layout->addWidget(minimize);
        layout->addWidget(maximize);
        layout->addWidget(close);
        connect(minimize, &QPushButton::clicked, this, [this] { window()->showMinimized(); });
        connect(maximize, &QPushButton::clicked, this, [this] {
            window()->isMaximized() ? window()->showNormal() : window()->showMaximized();
        });
        connect(close, &QPushButton::clicked, this, [this] { window()->close(); });
    }

protected:
    void mousePressEvent(QMouseEvent *event) override {
        if (event->button() == Qt::LeftButton && window()->windowHandle())
            window()->windowHandle()->startSystemMove();
        QWidget::mousePressEvent(event);
    }

    void mouseDoubleClickEvent(QMouseEvent *event) override {
        if (event->button() == Qt::LeftButton)
            window()->isMaximized() ? window()->showNormal() : window()->showMaximized();
        QWidget::mouseDoubleClickEvent(event);
    }

private:
    QPushButton *windowButton(const QString &text, const QString &kind) {
        auto *button = makeButton(text);
        button->setObjectName("window" + kind);
        button->setFixedSize(38, 28);
        button->setStyleSheet(kind == "close"
            ? "QPushButton { border: 0; background: transparent; color: #687588; font-size: 18px; padding: 0; } QPushButton:hover { background: #e81123; color: white; }"
            : "QPushButton { border: 0; background: transparent; color: #687588; font-size: 15px; padding: 0; } QPushButton:hover { background: #edf1f5; }");
        return button;
    }
};

class MainWindow final : public QMainWindow {
public:
    MainWindow() {
        setWindowTitle("Yiuk Device Assist");
        setWindowFlags(Qt::Window | Qt::FramelessWindowHint);
        resize(1060, 660);
        setMinimumSize(900, 600);
        setStyleSheet(R"(
            QMainWindow, QWidget#root { background: #ffffff; color: #182230; }
            QLabel { background: transparent; }
            QFrame#sidebar { background: #f1f4f8; border: none; }
            QLabel#muted { color: #687588; }
            QLabel#eyebrow { color: #66758a; font-size: 13px; }
            QLabel#sectionTitle { font-size: 18px; font-weight: 650; }
            QFrame#identityPanel { background: #f5f7fa; border: 1px solid #edf0f4; border-radius: 9px; }
            QFrame#field { background: #ffffff; border: 1px solid #cbd3de; border-radius: 7px; }
            QLineEdit { border: none; background: transparent; color: #182230; font-size: 17px; selection-background-color: #b9d7ff; }
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
        bodyLayout->addWidget(createContent(), 1);
        layout->addWidget(body, 1);
        setCentralWidget(root);
        setupSystemTray();
        startLocalBridge();
    }

private:
    QString deviceCode = spacedCode(randomDigits(9));
    QString temporaryPassword = randomDigits(8);
    QLabel *passwordValue = nullptr;
    QPushButton *togglePassword = nullptr;
    bool passwordVisible = false;
    QLineEdit *remoteCode = nullptr;
    QCheckBox *acceptingRequests = nullptr;
    QLabel *serviceState = nullptr;
    QLabel *serviceFooter = nullptr;
    QWidget *pendingPanel = nullptr;
    QVBoxLayout *pendingLayout = nullptr;
    LocalBridge *bridge = nullptr;
    QHash<QString, QJsonObject> sessions;
    QHash<QString, QJsonObject> tasks;
    QHash<QString, QProcess *> processes;
    QHash<QString, QString> taskFiles;
    QSystemTrayIcon *trayIcon = nullptr;
    QHash<QString, QString> notificationSessions;

    void setupSystemTray() {
        trayIcon = new QSystemTrayIcon(QApplication::style()->standardIcon(QStyle::SP_ComputerIcon), this);
        trayIcon->setToolTip("Yiuk Device Assist");
        auto *menu = new QMenu(this);
        auto *open = menu->addAction("打开 Yiuk Device Assist");
        auto *quit = menu->addAction("退出");
        connect(open, &QAction::triggered, this, [this] { showNormal(); activateWindow(); });
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

    QWidget *createSidebar() {
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

        auto *nav1 = navButton("⌂   设备协助", true);
        auto *nav2 = navButton("▣   我的设备");
        auto *nav3 = navButton("◷   会话记录");
        auto *nav4 = navButton("⚙   设置");
        layout->addWidget(nav1);
        layout->addWidget(nav2);
        layout->addWidget(nav3);
        layout->addWidget(nav4);
        connect(nav2, &QPushButton::clicked, this, [this] { showInfo("设备列表将在后续版本接入。当前页面用于验证远程协助主流程。 "); });
        connect(nav3, &QPushButton::clicked, this, [this] { showInfo("会话记录将在远程平台后端接入后显示。 "); });
        connect(nav4, &QPushButton::clicked, this, [this] { showInfo("客户端设置尚未接入。 "); });
        layout->addWidget(makeLabel("设备连接", "font-size: 12px; color: #69788d; padding: 8px 5px 2px;"));
        serviceState = makeLabel("● 本机 MCP 桥接启动中", "font-size: 11px; color: #b7791f; padding: 0 5px;");
        layout->addWidget(serviceState);
        layout->addStretch();
        layout->addWidget(makeLabel("本机验证版  ·  0.1.0", "font-size: 11px; color: #95a0af; padding: 0 4px;"));
        return sidebar;
    }

    QPushButton *navButton(const QString &text, bool active = false) {
        auto *button = makeButton(text);
        button->setObjectName("navItem");
        button->setCheckable(true);
        button->setChecked(active);
        button->setMinimumHeight(40);
        return button;
    }

    QWidget *createContent() {
        auto *content = new QWidget;
        auto *layout = new QVBoxLayout(content);
        layout->setContentsMargins(57, 26, 57, 24);
        layout->setSpacing(0);

        auto *accessHeader = new QHBoxLayout;
        acceptingRequests = new QCheckBox("允许本机接受协助请求");
        acceptingRequests->setChecked(true);
        acceptingRequests->setStyleSheet("font-size: 14px; font-weight: 600; color: #263446;");
        accessHeader->addWidget(acceptingRequests);
        accessHeader->addSpacing(12);
        auto *state = makeLabel("● 仅展示原型状态", "color: #b7791f; font-size: 12px; background: #fff6db; border-radius: 10px; padding: 5px 10px;");
        accessHeader->addWidget(state);
        accessHeader->addStretch();
        layout->addLayout(accessHeader);
        connect(acceptingRequests, &QCheckBox::toggled, this, [state](bool enabled) {
            state->setText(enabled ? "● 仅展示原型状态" : "● 暂停接受请求");
            state->setStyleSheet(enabled
                ? "color: #b7791f; font-size: 12px; background: #fff6db; border-radius: 10px; padding: 5px 10px;"
                : "color: #687588; font-size: 12px; background: #edf0f4; border-radius: 10px; padding: 5px 10px;");
        });
        layout->addSpacing(14);

        auto *identity = new QFrame;
        identity->setObjectName("identityPanel");
        auto *identityLayout = new QHBoxLayout(identity);
        identityLayout->setContentsMargins(22, 18, 22, 18);
        identityLayout->setSpacing(18);
        auto *deviceColumn = new QVBoxLayout;
        deviceColumn->setSpacing(9);
        deviceColumn->addWidget(makeLabel("本机设备码", "color: #758297; font-size: 13px;"));
        auto *codeRow = new QHBoxLayout;
        auto *code = makeLabel(deviceCode, "font-size: 27px; font-weight: 500; color: #172435;");
        codeRow->addWidget(code);
        codeRow->addSpacing(5);
        auto *copyCode = makeButton("複製");
        copyCode->setObjectName("subtle");
        copyCode->setToolTip("复制设备码");
        codeRow->addWidget(copyCode);
        codeRow->addStretch();
        deviceColumn->addLayout(codeRow);
        identityLayout->addLayout(deviceColumn, 1);

        auto *separator = new QFrame;
        separator->setFrameShape(QFrame::VLine);
        separator->setStyleSheet("color: #e0e5eb;");
        identityLayout->addWidget(separator);

        auto *passwordColumn = new QVBoxLayout;
        passwordColumn->setSpacing(9);
        passwordColumn->addWidget(makeLabel("连接凭证 · 临时密码", "color: #758297; font-size: 13px;"));
        auto *passwordRow = new QHBoxLayout;
        passwordValue = makeLabel("********", "font-size: 24px; letter-spacing: 3px; color: #172435;");
        passwordValue->setFixedWidth(150);
        passwordValue->setAlignment(Qt::AlignLeft | Qt::AlignVCenter);
        passwordRow->addWidget(passwordValue);
        auto *passwordActions = new QHBoxLayout;
        passwordActions->setContentsMargins(0, 0, 0, 0);
        passwordActions->setSpacing(2);
        auto *copyPassword = makeButton("复制");
        copyPassword->setObjectName("subtle");
        copyPassword->setToolTip("复制临时密码");
        auto *refreshPassword = makeButton("↻");
        refreshPassword->setObjectName("subtle");
        refreshPassword->setToolTip("生成新的临时密码");
        passwordActions->addWidget(copyPassword);
        passwordActions->addWidget(refreshPassword);
        togglePassword = new QPushButton;
        togglePassword->setObjectName("passwordEye");
        togglePassword->setCheckable(true);
        togglePassword->setFixedSize(30, 30);
        togglePassword->setIconSize(QSize(18, 18));
        togglePassword->setIcon(QIcon(":/icons/eye.svg"));
        togglePassword->setToolTip("显示临时密码");
        togglePassword->setAccessibleName("显示或隐藏临时密码");
        passwordActions->addWidget(togglePassword);
        passwordActions->addStretch();
        passwordActions->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
        passwordRow->addLayout(passwordActions);
        passwordRow->addStretch();
        passwordColumn->addLayout(passwordRow);
        identityLayout->addLayout(passwordColumn, 1);
        layout->addWidget(identity);

        connect(copyCode, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
        connect(togglePassword, &QPushButton::toggled, this, [this](bool visible) {
            passwordVisible = visible;
            passwordValue->setText(passwordVisible ? temporaryPassword : "********");
            togglePassword->setIcon(QIcon(passwordVisible ? ":/icons/eye-off.svg" : ":/icons/eye.svg"));
            togglePassword->setToolTip(passwordVisible ? "隐藏临时密码" : "显示临时密码");
        });
        connect(copyPassword, &QPushButton::clicked, this, [this] { copyToClipboard(temporaryPassword, "临时密码已复制"); });
        connect(refreshPassword, &QPushButton::clicked, this, [this] {
            temporaryPassword = randomDigits(8);
            passwordValue->setText(passwordVisible ? temporaryPassword : "********");
            showInfo("原型密码已更新。真实凭证服务尚未接入。");
        });

        layout->addSpacing(40);
        layout->addWidget(makeLabel("连接其他设备", "font-size: 19px; font-weight: 650;"));
        layout->addSpacing(7);
        layout->addWidget(makeLabel("输入对方设备码，发送协助请求并等待对方确认。", "color: #788598; font-size: 13px;"));
        layout->addSpacing(15);

        auto *connectRow = new QHBoxLayout;
        auto *field = new QFrame;
        field->setObjectName("field");
        auto *fieldLayout = new QHBoxLayout(field);
        fieldLayout->setContentsMargins(14, 1, 12, 1);
        remoteCode = new QLineEdit;
        remoteCode->setPlaceholderText("输入 9 位设备码");
        remoteCode->setMaxLength(11);
        fieldLayout->addWidget(remoteCode);
        connectRow->addWidget(field, 1);
        auto *connectButton = makeButton("申请连接");
        connectButton->setObjectName("primary");
        connectButton->setMinimumWidth(145);
        connectButton->setMinimumHeight(48);
        connectRow->addWidget(connectButton);
        layout->addLayout(connectRow);
        connect(remoteCode, &QLineEdit::textChanged, this, [this](const QString &text) {
            QString digits;
            for (const QChar ch : text)
                if (ch.isDigit() && digits.size() < 9) digits += ch;
            if (digits != text) {
                const int cursor = remoteCode->cursorPosition();
                remoteCode->setText(digits);
                remoteCode->setCursorPosition(qMin(cursor, digits.size()));
            }
        });
        connect(connectButton, &QPushButton::clicked, this, [this] {
            QString digits;
            for (const QChar ch : remoteCode->text()) if (ch.isDigit()) digits += ch;
            if (digits.size() != 9) {
                QMessageBox::warning(this, "设备码无效", "请输入完整的 9 位设备码。");
                return;
            }
            if (!acceptingRequests->isChecked()) {
                QMessageBox::information(this, "本机已暂停", "请先开启“允许本机接受协助请求”。");
                return;
            }
            QMessageBox::information(this, "后端尚未接通", "已输入设备码 " + spacedCode(digits) + "。\n\n连接申请功能将在设备身份和会话服务实现后启用。当前不会发起真实连接。");
        });

        layout->addSpacing(25);
        pendingPanel = new QWidget;
        pendingLayout = new QVBoxLayout(pendingPanel);
        pendingLayout->setContentsMargins(0, 0, 0, 0);
        pendingLayout->setSpacing(8);
        layout->addWidget(pendingPanel);

        auto *notice = new QFrame;
        notice->setStyleSheet("background: #f6f9fc; border: 1px solid #e7edf3; border-radius: 7px;");
        auto *noticeLayout = new QHBoxLayout(notice);
        noticeLayout->setContentsMargins(15, 12, 15, 12);
        noticeLayout->addWidget(makeLabel("i", "color: #1977eb; font-size: 15px; font-weight: 700;"));
        noticeLayout->addSpacing(5);
        noticeLayout->addWidget(makeLabel("每次遠程協助都需要设备所有者明确批准；会话期间可随时停止。", "color: #647286; font-size: 12px;"));
        noticeLayout->addStretch();
        layout->addWidget(notice);
        layout->addStretch();

        auto *footer = new QHBoxLayout;
        footer->addWidget(makeLabel("服务状态", "color: #738197; font-size: 12px;"));
        serviceFooter = makeLabel("● 等待本机 MCP 调用", "color: #16825d; font-size: 12px;");
        footer->addWidget(serviceFooter);
        footer->addStretch();
        footer->addWidget(makeLabel("远程命令能力尚未启用", "color: #929dad; font-size: 12px;"));
        layout->addLayout(footer);
        return content;
    }

    static QJsonObject ok(const QJsonValue &data = {}) {
        QJsonObject result{{"ok", true}, {"code", "OK"}};
        if (!data.isUndefined()) result.insert("data", data);
        return result;
    }

    static QJsonObject fail(const QString &code, const QString &message) {
        return {{"ok", false}, {"code", code}, {"message", message}};
    }

    QString deviceId() const {
        QString id = deviceCode;
        id.remove(' ');
        return id;
    }

    QJsonObject deviceRecord() const {
        return {{"device_id", deviceId()}, {"name", "This Windows device"},
                {"online", bridge && bridge->isListening()}, {"platform", "windows"},
                {"backend", "local-demo"}};
    }

    void startLocalBridge() {
        bridge = new LocalBridge([this](const QString &tool, const QJsonObject &input) {
            return invokeLocalTool(tool, input);
        }, this);
        QString error;
        if (!bridge->start(&error)) {
            serviceState->setText("● 本机 MCP 桥接启动失败");
            serviceState->setStyleSheet("font-size: 11px; color: #b42318; padding: 0 5px;");
            serviceFooter->setText("● 本机 API 启动失败");
            return;
        }
        const QString configRoot = QDir(qEnvironmentVariable("APPDATA")).filePath("Yiuk/DeviceAssist");
        if (!QDir().mkpath(configRoot)) {
            serviceState->setText("● MCP 配置目录创建失败");
            serviceFooter->setText("● 无法创建本机配置目录");
            return;
        }
        QFile config(QDir(configRoot).filePath("local-api.json"));
        if (!config.open(QIODevice::WriteOnly | QIODevice::Truncate)) {
            serviceState->setText("● MCP 配置写入失败");
            serviceFooter->setText(config.errorString());
            return;
        }
        const QJsonObject configObject{{"url", bridge->endpoint()}, {"token", bridge->token()}};
        if (config.write(QJsonDocument(configObject).toJson(QJsonDocument::Compact)) < 0) {
            serviceState->setText("● MCP 配置写入失败");
            serviceFooter->setText(config.errorString());
            return;
        }
        config.close();
        serviceState->setText("● 本机 MCP 桥接就绪");
        serviceState->setStyleSheet("font-size: 11px; color: #16825d; padding: 0 5px;");
        serviceFooter->setText("● MCP 已连接等待调用");
    }

    QJsonObject invokeLocalTool(const QString &tool, const QJsonObject &input) {
        if (!bridge || !bridge->isListening()) return fail("LOCAL_CLIENT_UNAVAILABLE", "本机 Qt 客户端桥接未启动");
        if (tool == "list_devices") return ok(QJsonArray{deviceRecord()});
        if (tool == "get_device_status") {
            if (input.value("device_id").toString() != deviceId()) return fail("DEVICE_NOT_FOUND", "本机演示桥只注册当前设备");
            QJsonObject status = deviceRecord();
            status.insert("active_sessions", sessions.size());
            return ok(status);
        }
        if (tool == "request_connection") return requestLocalSession(input);
        if (tool == "get_session_status") return sessionStatus(input.value("session_id").toString());
        if (tool == "execute_command") return executeLocalCommand(input);
        if (tool == "get_command_result") return commandResult(input);
        if (tool == "cancel_command") return cancelCommand(input);
        if (tool == "close_session") return closeSession(input.value("session_id").toString());
        return fail("UNKNOWN_TOOL", "未支持的工具");
    }

    QJsonObject requestLocalSession(const QJsonObject &input) {
        const QString target = input.value("device_id").toString();
        if (target != deviceId()) return fail("DEVICE_NOT_FOUND", "本机演示桥仅可连接当前设备");
        if (!acceptingRequests->isChecked()) return fail("DEVICE_UNAVAILABLE", "本机当前暂停接受协助请求");
        const QString id = QUuid::createUuid().toString(QUuid::WithoutBraces);
        QJsonObject session{{"session_id", id}, {"device_id", target}, {"purpose", input.value("purpose").toString()},
                            {"permissions", input.value("permissions")}, {"status", "pending_approval"},
                            {"created_at", QDateTime::currentDateTimeUtc().toString(Qt::ISODate)}};
        sessions.insert(id, session);
        refreshPendingRequests();
        serviceFooter->setText("● 有待批准的 MCP 连接申请");
        notificationSessions.insert(id, input.value("purpose").toString());
        if (trayIcon && QSystemTrayIcon::supportsMessages()) {
            trayIcon->showMessage("需要批准远程协助", input.value("purpose").toString() + "\n点击此通知查看并批准或拒绝。",
                                  QSystemTrayIcon::Information, 10000);
        } else {
            QTimer::singleShot(0, this, [this, id] { showApprovalDialog(id); });
        }
        return ok(session);
    }

    QJsonObject sessionStatus(const QString &id) const {
        if (!sessions.contains(id)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
        return ok(sessions.value(id));
    }

    void refreshPendingRequests() {
        while (QLayoutItem *item = pendingLayout->takeAt(0)) {
            if (item->widget()) item->widget()->deleteLater();
            delete item;
        }
        for (auto it = sessions.cbegin(); it != sessions.cend(); ++it) {
            const QJsonObject session = it.value();
            if (session.value("status").toString() != "pending_approval") continue;
            auto *card = new QFrame;
            card->setStyleSheet("QFrame { background: #fff9e8; border: 1px solid #f2d58a; border-radius: 7px; }");
            auto *row = new QHBoxLayout(card);
            row->setContentsMargins(12, 9, 10, 9);
            auto *text = makeLabel("Agent 申请本机协助：" + session.value("purpose").toString(), "color: #49566a; font-size: 12px;");
            text->setWordWrap(true);
            row->addWidget(text, 1);
            auto *approve = makeButton("批准");
            auto *reject = makeButton("拒绝");
            approve->setStyleSheet("QPushButton { background: #1479ed; color: white; border: 0; padding: 7px 13px; } QPushButton:hover { background: #096bd7; }");
            reject->setObjectName("subtle");
            row->addWidget(approve);
            row->addWidget(reject);
            const QString id = it.key();
            connect(approve, &QPushButton::clicked, this, [this, id] { decideSession(id, true); });
            connect(reject, &QPushButton::clicked, this, [this, id] { decideSession(id, false); });
            pendingLayout->addWidget(card);
        }
    }

    void showApprovalDialog(const QString &id) {
        if (!sessions.contains(id) || sessions.value(id).value("status").toString() != "pending_approval") return;
        const QJsonObject session = sessions.value(id);
        QMessageBox dialog(this);
        dialog.setWindowTitle("远程协助授权");
        dialog.setIcon(QMessageBox::Question);
        dialog.setText("Agent 请求访问本机");
        dialog.setInformativeText(QString("用途：%1\n申请权限：执行单条 PowerShell 命令\n\n仅在你批准后，Agent 才能提交命令。你可以随时关闭会话。")
                                      .arg(session.value("purpose").toString()));
        QPushButton *approve = dialog.addButton("批准本次会话", QMessageBox::AcceptRole);
        dialog.addButton("拒绝", QMessageBox::RejectRole);
        dialog.setDefaultButton(qobject_cast<QPushButton *>(dialog.buttons().last()));
        dialog.exec();
        decideSession(id, dialog.clickedButton() == approve);
        showNormal();
        activateWindow();
    }

    void decideSession(const QString &id, bool approved) {
        if (!sessions.contains(id)) return;
        QJsonObject session = sessions.value(id);
        if (session.value("status").toString() != "pending_approval") return;
        session.insert("status", approved ? "active" : "rejected");
        session.insert("decided_at", QDateTime::currentDateTimeUtc().toString(Qt::ISODate));
        sessions.insert(id, session);
        refreshPendingRequests();
        serviceFooter->setText(approved ? "● 本机会话已批准" : "● 连接申请已拒绝");
    }

    QJsonObject executeLocalCommand(const QJsonObject &input) {
        const QString sessionId = input.value("session_id").toString();
        if (!sessions.contains(sessionId)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
        if (sessions.value(sessionId).value("status").toString() != "active") return fail("PERMISSION_DENIED", "设备所有者尚未批准此会话");
        const QString command = input.value("command").toString();
        if (command.trimmed().isEmpty() || command.size() > 32768) return fail("INVALID_ARGUMENT", "命令为空或超过 32 KiB");
        const QString requestId = QUuid::createUuid().toString(QUuid::WithoutBraces);
        const int timeout = qBound(1, input.value("timeout_seconds").toInt(60), 3600);
        auto *process = new QProcess(this);
        process->setProcessChannelMode(QProcess::SeparateChannels);
        const QString scriptPath = QDir(QStandardPaths::writableLocation(QStandardPaths::TempLocation)).filePath("yiuk-" + requestId + ".ps1");
        QFile script(scriptPath);
        if (!script.open(QIODevice::WriteOnly)) {
            process->deleteLater();
            return fail("EXECUTION_FAILED", "无法创建临时 PowerShell 脚本");
        }
        script.write(QByteArray::fromHex("efbbbf"));
        script.write("[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false\r\n$OutputEncoding = [Console]::OutputEncoding\r\n");
        script.write(command.toUtf8());
        script.close();
        QString program = QStandardPaths::findExecutable("pwsh.exe");
        if (program.isEmpty()) program = QStandardPaths::findExecutable("powershell.exe");
        if (program.isEmpty()) {
            QFile::remove(scriptPath);
            process->deleteLater();
            return fail("EXECUTION_FAILED", "未找到 PowerShell 可执行程序");
        }
        QStringList args{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath};
        process->setProgram(program);
        process->setArguments(args);
        const QString cwd = input.value("working_directory").toString();
        if (!cwd.isEmpty() && QDir(cwd).exists()) process->setWorkingDirectory(cwd);
        tasks.insert(requestId, {{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"}, {"stdout", ""}, {"stderr", ""}, {"exit_code", QJsonValue(QJsonValue::Null)}, {"truncated", false}});
        processes.insert(requestId, process);
        taskFiles.insert(requestId, scriptPath);
        auto appendOutput = [this, requestId](QProcess *p, bool standardError) {
            QByteArray bytes = standardError ? p->readAllStandardError() : p->readAllStandardOutput();
            QJsonObject task = tasks.value(requestId);
            const QString key = standardError ? "stderr" : "stdout";
            QString current = task.value(key).toString();
            constexpr qsizetype limit = 1 << 20;
            const qsizetype available = qMax<qsizetype>(0, limit - current.toUtf8().size());
            if (bytes.size() > available) {
                bytes.truncate(available);
                task.insert("truncated", true);
            }
            current += QString::fromUtf8(bytes);
            task.insert(key, current);
            tasks.insert(requestId, task);
        };
        connect(process, &QProcess::readyReadStandardOutput, this, [appendOutput, process] { appendOutput(process, false); });
        connect(process, &QProcess::readyReadStandardError, this, [appendOutput, process] { appendOutput(process, true); });
        connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
                [this, requestId, process, appendOutput](int exitCode, QProcess::ExitStatus exitStatus) {
            appendOutput(process, false);
            appendOutput(process, true);
            QJsonObject task = tasks.value(requestId);
            const QString currentStatus = task.value("status").toString();
            if (currentStatus == "cancelled" || currentStatus == "timed_out") return;
            task.insert("status", exitStatus == QProcess::NormalExit && exitCode == 0 ? "succeeded" : "failed");
            task.insert("exit_code", exitCode);
            tasks.insert(requestId, task);
            QFile::remove(taskFiles.take(requestId));
            processes.remove(requestId);
            process->deleteLater();
            serviceFooter->setText("● PowerShell 任务完成");
        });
        connect(process, &QProcess::errorOccurred, this, [this, requestId, process](QProcess::ProcessError) {
            if (process->state() != QProcess::NotRunning) return;
            QJsonObject task = tasks.value(requestId);
            task.insert("status", "failed");
            task.insert("stderr", task.value("stderr").toString() + process->errorString());
            tasks.insert(requestId, task);
            QFile::remove(taskFiles.take(requestId));
            processes.remove(requestId);
            process->deleteLater();
        });
        process->start();
        QTimer::singleShot(timeout * 1000, this, [this, requestId] {
            if (!processes.contains(requestId)) return;
            QProcess *process = processes.value(requestId);
            if (process->state() == QProcess::NotRunning) return;
            QJsonObject task = tasks.value(requestId);
            task.insert("status", "timed_out");
            tasks.insert(requestId, task);
            process->kill();
            QFile::remove(taskFiles.take(requestId));
            processes.remove(requestId);
        });
        serviceFooter->setText("● 正在执行本机 PowerShell 任务");
        return ok(QJsonObject{{"session_id", sessionId}, {"request_id", requestId}, {"status", "running"}});
    }

    QJsonObject commandResult(const QJsonObject &input) const {
        const QString requestId = input.value("request_id").toString();
        if (!tasks.contains(requestId) || tasks.value(requestId).value("session_id").toString() != input.value("session_id").toString())
            return fail("TASK_NOT_FOUND", "任务不存在或不属于该会话");
        QJsonObject result = tasks.value(requestId);
        const int cursor = qMax(0, input.value("cursor").toInt());
        result.insert("stdout", result.value("stdout").toString().mid(cursor));
        return ok(result);
    }

    QJsonObject cancelCommand(const QJsonObject &input) {
        const QString requestId = input.value("request_id").toString();
        if (!tasks.contains(requestId) || tasks.value(requestId).value("session_id").toString() != input.value("session_id").toString())
            return fail("TASK_NOT_FOUND", "任务不存在或不属于该会话");
        if (processes.contains(requestId)) {
            QProcess *process = processes.take(requestId);
            process->kill();
            process->deleteLater();
            QFile::remove(taskFiles.take(requestId));
        }
        QJsonObject task = tasks.value(requestId);
        task.insert("status", "cancelled");
        tasks.insert(requestId, task);
        return ok(QJsonObject{{"request_id", requestId}, {"status", "cancelled"}});
    }

    QJsonObject closeSession(const QString &id) {
        if (!sessions.contains(id)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
        QJsonObject session = sessions.value(id);
        session.insert("status", "closed");
        sessions.insert(id, session);
        for (auto it = tasks.begin(); it != tasks.end(); ++it) {
            if (it.value().value("session_id").toString() == id && processes.contains(it.key())) {
                QProcess *process = processes.take(it.key());
                process->kill();
                process->deleteLater();
                QFile::remove(taskFiles.take(it.key()));
                QJsonObject task = it.value();
                task.insert("status", "cancelled");
                it.value() = task;
            }
        }
        refreshPendingRequests();
        serviceFooter->setText("● 本机会话已关闭");
        return ok(session);
    }

    void copyToClipboard(const QString &text, const QString &message) {
        QApplication::clipboard()->setText(text);
        statusBar()->showMessage(message, 1800);
    }

    void showInfo(const QString &message) {
        QMessageBox::information(this, "Yiuk Device Assist", message);
    }
};

} // namespace

int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    app.setApplicationName("Yiuk Device Assist");
    app.setOrganizationName("Yiuk");
    MainWindow window;
    window.show();
    return app.exec();
}
