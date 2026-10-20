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
#include <QPainter>
#include <QPainterPath>
#include <QtMath>
#include <QProcess>
#include <QStandardPaths>
#include <QSettings>
#include <QStackedWidget>
#include <QTemporaryFile>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QUuid>
#include <QPushButton>
#include <QRandomGenerator>
#include <QStatusBar>
#include <QStyle>
#include <QSysInfo>
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

QString jsonArrayText(const QJsonArray &array) {
    QStringList values;
    for (const QJsonValue &value : array) values.append(value.toString());
    return values.join(", ");
}

enum class Glyph {
    Home, Devices, History, Settings, Copy, Refresh, Star, Eye, EyeOff, Minimize, Maximize, Restore, Close
};

void paintGlyph(QPainter &painter, Glyph glyph, const QColor &color) {
    painter.setRenderHint(QPainter::Antialiasing, true);
    painter.setPen(QPen(color, 1.65, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));
    painter.setBrush(Qt::NoBrush);
    switch (glyph) {
    case Glyph::Home: {
        QPainterPath path; path.moveTo(2, 8); path.lineTo(9, 2); path.lineTo(16, 8);
        path.moveTo(4, 7); path.lineTo(4, 16); path.lineTo(14, 16); path.lineTo(14, 7);
        path.moveTo(8, 16); path.lineTo(8, 11); path.lineTo(11, 11); path.lineTo(11, 16); painter.drawPath(path); break;
    }
    case Glyph::Devices:
        painter.drawRoundedRect(QRectF(2, 3, 11, 9), 1.5, 1.5);
        painter.drawLine(QPointF(5, 15), QPointF(10, 15)); painter.drawLine(QPointF(7.5, 12), QPointF(7.5, 15));
        painter.drawRoundedRect(QRectF(12, 8, 4, 8), 1, 1); break;
    case Glyph::History:
        painter.drawEllipse(QRectF(2.5, 2.5, 13, 13));
        painter.drawLine(QPointF(9, 5), QPointF(9, 9)); painter.drawLine(QPointF(9, 9), QPointF(12, 10.5)); break;
    case Glyph::Settings:
        painter.drawEllipse(QRectF(6, 6, 6, 6));
        for (int a = 0; a < 360; a += 45) {
            const qreal r1 = 6.7, r2 = 8.4, rad = a * 3.14159265358979323846 / 180.0;
            painter.drawLine(QPointF(9 + qCos(rad) * r1, 9 + qSin(rad) * r1), QPointF(9 + qCos(rad) * r2, 9 + qSin(rad) * r2));
        }
        break;
    case Glyph::Copy:
        painter.drawRoundedRect(QRectF(6, 2, 9, 11), 1.4, 1.4);
        painter.drawLine(QPointF(4, 5), QPointF(3, 5)); painter.drawLine(QPointF(3, 5), QPointF(3, 15));
        painter.drawLine(QPointF(3, 15), QPointF(12, 15)); painter.drawLine(QPointF(12, 15), QPointF(12, 14)); break;
    case Glyph::Refresh:
        painter.drawArc(QRectF(3, 3, 12, 12), 40 * 16, 245 * 16);
        painter.drawLine(QPointF(13, 3), QPointF(16, 3)); painter.drawLine(QPointF(16, 3), QPointF(16, 6));
        painter.drawLine(QPointF(5, 15), QPointF(2, 15)); painter.drawLine(QPointF(2, 15), QPointF(2, 12)); break;
    case Glyph::Star: {
        QPainterPath path;
        for (int i = 0; i < 10; ++i) {
            const qreal angle = -3.14159265358979323846 / 2 + i * 3.14159265358979323846 / 5;
            const qreal radius = i % 2 == 0 ? 7.2 : 3.2;
            const QPointF point(9 + qCos(angle) * radius, 9 + qSin(angle) * radius);
            if (i == 0) path.moveTo(point); else path.lineTo(point);
        }
        path.closeSubpath();
        painter.drawPath(path);
        break;
    }
    case Glyph::Eye:
    case Glyph::EyeOff: {
        QPainterPath path; path.moveTo(1.5, 9); path.cubicTo(5, 3, 13, 3, 16.5, 9); path.cubicTo(13, 15, 5, 15, 1.5, 9); painter.drawPath(path);
        painter.drawEllipse(QRectF(7, 7, 4, 4));
        if (glyph == Glyph::EyeOff) painter.drawLine(QPointF(3, 3), QPointF(15, 15));
        break;
    }
    case Glyph::Minimize: painter.drawLine(QPointF(3, 12), QPointF(15, 12)); break;
    case Glyph::Maximize: painter.drawRect(QRectF(3.5, 3.5, 11, 11)); break;
    case Glyph::Restore:
        painter.drawRect(QRectF(3.5, 5.5, 9, 9));
        painter.drawLine(QPointF(6, 3.5), QPointF(14.5, 3.5)); painter.drawLine(QPointF(14.5, 3.5), QPointF(14.5, 12)); break;
    case Glyph::Close:
        painter.drawLine(QPointF(4, 4), QPointF(14, 14)); painter.drawLine(QPointF(14, 4), QPointF(4, 14)); break;
    }
}

QPixmap glyphPixmap(Glyph glyph, const QColor &color) {
    QPixmap pixmap(18, 18);
    pixmap.fill(Qt::transparent);
    QPainter painter(&pixmap);
    painter.setRenderHint(QPainter::Antialiasing, true);
    painter.translate(2.0, 2.0);
    painter.scale(0.78, 0.78);
    paintGlyph(painter, glyph, color);
    return pixmap;
}

QIcon glyphIcon(Glyph glyph) {
    QIcon icon;
    icon.addPixmap(glyphPixmap(glyph, QColor("#66758a")), QIcon::Normal, QIcon::Off);
    icon.addPixmap(glyphPixmap(glyph, QColor("#1479ed")), QIcon::Active, QIcon::Off);
    icon.addPixmap(glyphPixmap(glyph, QColor("#1479ed")), QIcon::Normal, QIcon::On);
    icon.addPixmap(glyphPixmap(glyph, QColor("#aab4c1")), QIcon::Disabled, QIcon::Off);
    return icon;
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
        auto *minimize = windowButton(Glyph::Minimize, "minimize", "最小化");
        auto *maximize = windowButton(Glyph::Maximize, "maximize", "最大化");
        auto *close = windowButton(Glyph::Close, "close", "关闭");
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
    QPushButton *windowButton(Glyph glyph, const QString &kind, const QString &tip) {
        auto *button = new QPushButton;
        button->setObjectName("window" + kind);
        button->setIcon(glyphIcon(glyph));
        button->setIconSize(QSize(18, 18));
        button->setToolTip(tip);
        button->setCursor(Qt::PointingHandCursor);
        button->setFixedSize(38, 30);
        button->setStyleSheet(kind == "close"
            ? "QPushButton { border: 0; background: transparent; padding: 0; } QPushButton:hover { background: #e81123; border-radius: 4px; }"
            : "QPushButton { border: 0; background: transparent; padding: 0; } QPushButton:hover { background: #edf1f5; border-radius: 4px; }");
        return button;
    }
};

class MainWindow final : public QMainWindow {
public:
    MainWindow() {
        setWindowTitle("Yiuk Device Assist");
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
        QTimer::singleShot(0, this, [this] {
            if (acceptingRequests && acceptingRequests->isChecked() && !QSettings().value("localDemoConsent", false).toBool())
                confirmLocalDemoConsent();
        });
    }

private:
    QString deviceCode = loadOrCreateDeviceCode();
    QString temporaryPassword = randomDigits(8);
    QLabel *passwordValue = nullptr;
    QPushButton *togglePassword = nullptr;
    bool passwordVisible = false;
    QLineEdit *remoteCode = nullptr;
    QCheckBox *acceptingRequests = nullptr;
    QStackedWidget *pages = nullptr;
    QPushButton *navHome = nullptr;
    QPushButton *navDevices = nullptr;
    QPushButton *navHistory = nullptr;
    QPushButton *navSettings = nullptr;
    QLabel *deviceIdValue = nullptr;
    QLabel *deviceStatusValue = nullptr;
    QLabel *sessionSummaryValue = nullptr;
    QLabel *settingsStatusValue = nullptr;
    QLabel *sessionsHistory = nullptr;
    QWidget *homePage = nullptr;
    QWidget *devicesPage = nullptr;
    QWidget *sessionsPage = nullptr;
    QWidget *settingsPage = nullptr;
    QLabel *serviceState = nullptr;
    QLabel *serviceFooter = nullptr;
    QWidget *pendingPanel = nullptr;
    QVBoxLayout *pendingLayout = nullptr;
    QWidget *activePanel = nullptr;
    QVBoxLayout *activeLayout = nullptr;
    QLabel *platformState = nullptr;
    QLabel *mcpState = nullptr;
    QLabel *deviceState = nullptr;
    QTimer *sessionTimer = nullptr;
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

    static QString loadOrCreateDeviceCode() {
        const QString configRoot = QDir(qEnvironmentVariable("APPDATA")).filePath("Yiuk/DeviceAssist");
        QDir().mkpath(configRoot);
        const QString path = QDir(configRoot).filePath("device.json");
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

    QPushButton *navButton(const QString &text, Glyph glyph, bool active = false) {
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

    QPushButton *iconButton(Glyph glyph, const QString &tooltip) {
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

    QString sessionStatePath() const {
        return QDir(qEnvironmentVariable("APPDATA")).filePath("Yiuk/DeviceAssist/sessions.json");
    }

    void loadSessionState() {
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

    void persistSessionState() {
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

    bool confirmLocalDemoConsent() {
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

    QWidget *createHomePage() {
        auto *content = new QWidget;
        auto *layout = new QVBoxLayout(content);
        layout->setContentsMargins(57, 22, 57, 22);
        layout->setSpacing(0);
        auto *heading = new QHBoxLayout;
        heading->addWidget(makeLabel("主页", "font-size: 22px; font-weight: 650; color: #1d2939;"));
        heading->addStretch();
        layout->addLayout(heading);
        layout->addSpacing(18);

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
        auto *copyCode = iconButton(Glyph::Copy, "复制设备码");
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
        passwordColumn->addWidget(makeLabel("连接密码", "color: #758297; font-size: 13px;"));
        auto *passwordRow = new QHBoxLayout;
        passwordValue = makeLabel("********", "font-size: 24px; letter-spacing: 3px; color: #172435;");
        passwordValue->setFixedWidth(150);
        passwordValue->setAlignment(Qt::AlignLeft | Qt::AlignVCenter);
        passwordRow->addWidget(passwordValue);
        auto *copyPassword = iconButton(Glyph::Copy, "复制密码");
        togglePassword = iconButton(Glyph::Eye, "显示密码");
        togglePassword->setObjectName("passwordEye");
        togglePassword->setCheckable(true);
        passwordRow->addWidget(copyPassword);
        passwordRow->addWidget(togglePassword);
        passwordRow->addStretch();
        auto *refreshPassword = iconButton(Glyph::Refresh, "重新生成密码");
        passwordRow->addWidget(refreshPassword);
        passwordColumn->addLayout(passwordRow);
        identityLayout->addLayout(passwordColumn, 1);
        layout->addWidget(identity);

        connect(copyCode, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
        connect(togglePassword, &QPushButton::toggled, this, [this](bool visible) {
            passwordVisible = visible;
            passwordValue->setText(passwordVisible ? temporaryPassword : "********");
            togglePassword->setIcon(glyphIcon(passwordVisible ? Glyph::EyeOff : Glyph::Eye));
            togglePassword->setToolTip(passwordVisible ? "隐藏密码" : "显示密码");
        });
        connect(copyPassword, &QPushButton::clicked, this, [this] { copyToClipboard(temporaryPassword, "密码已复制"); });
        connect(refreshPassword, &QPushButton::clicked, this, [this] {
            temporaryPassword = randomDigits(8);
            passwordValue->setText(passwordVisible ? temporaryPassword : "********");
            statusBar()->showMessage("密码已更新", 1800);
        });

        layout->addSpacing(40);
        layout->addWidget(makeLabel("连接其他设备", "font-size: 19px; font-weight: 650;"));
        layout->addSpacing(7);
        layout->addWidget(makeLabel("输入对方设备码，发送协助请求并等待对方确认。", "color: #788598; font-size: 13px;"));
        layout->addSpacing(15);

        auto *connectRow = new QHBoxLayout;
        auto *field = new QFrame;
        field->setObjectName("field");
        field->setMaximumWidth(420);
        auto *fieldLayout = new QHBoxLayout(field);
        fieldLayout->setContentsMargins(14, 1, 12, 1);
        remoteCode = new QLineEdit;
        remoteCode->setPlaceholderText("例如 123 456 789");
        remoteCode->setMaxLength(11);
        fieldLayout->addWidget(remoteCode);
        connectRow->addWidget(field, 0);
        connectRow->addStretch();
        auto *connectButton = makeButton("申请连接");
        connectButton->setObjectName("primary");
        connectButton->setMinimumWidth(145);
        connectButton->setMinimumHeight(48);
        connectRow->addWidget(connectButton);
        layout->addLayout(connectRow);
        connect(remoteCode, &QLineEdit::textChanged, this, [this](const QString &text) {
            QString digits;
            int digitsBeforeCursor = 0;
            const int cursor = remoteCode->cursorPosition();
            for (int i = 0; i < text.size(); ++i) {
                if (text.at(i).isDigit() && digits.size() < 9) {
                    digits += text.at(i);
                    if (i < cursor) ++digitsBeforeCursor;
                }
            }
            QString formatted;
            for (int i = 0; i < digits.size(); ++i) {
                if (i == 3 || i == 6) formatted += ' ';
                formatted += digits.at(i);
            }
            if (formatted != text) {
                remoteCode->blockSignals(true);
                remoteCode->setText(formatted);
                int newCursor = digitsBeforeCursor;
                if (digitsBeforeCursor > 3) ++newCursor;
                if (digitsBeforeCursor > 6) ++newCursor;
                remoteCode->setCursorPosition(qMin(newCursor, formatted.size()));
                remoteCode->blockSignals(false);
            }
        });
        connect(connectButton, &QPushButton::clicked, this, [this] {
            QString digits;
            for (const QChar ch : remoteCode->text()) if (ch.isDigit()) digits += ch;
            if (digits.size() != 9) {
                QMessageBox::warning(this, "设备码无效", "请输入完整的 9 位设备码。");
                return;
            }
            QMessageBox::information(this, "远程平台未连接", "设备码：" + spacedCode(digits) + "\n\n当前无法发起连接请求。");
        });

        layout->addSpacing(25);
        pendingPanel = new QWidget;
        pendingLayout = new QVBoxLayout(pendingPanel);
        pendingLayout->setContentsMargins(0, 0, 0, 0);
        pendingLayout->setSpacing(8);
        layout->addWidget(pendingPanel);
        activePanel = new QWidget;
        activeLayout = new QVBoxLayout(activePanel);
        activeLayout->setContentsMargins(0, 0, 0, 0);
        activeLayout->setSpacing(8);
        layout->addWidget(activePanel);
        sessionTimer = new QTimer(this);
        sessionTimer->setInterval(1000);
        connect(sessionTimer, &QTimer::timeout, this, [this] { refreshActiveSessions(); });
        sessionTimer->start();

        auto *notice = new QFrame;
        notice->setStyleSheet("background: #f6f9fc; border: 1px solid #e7edf3; border-radius: 7px;");
        auto *noticeLayout = new QHBoxLayout(notice);
        noticeLayout->setContentsMargins(15, 12, 15, 12);
        noticeLayout->addWidget(makeLabel("i", "color: #1977eb; font-size: 15px; font-weight: 700;"));
        noticeLayout->addSpacing(5);
        noticeLayout->addWidget(makeLabel("每次远程协助都需要设备所有者明确批准；会话期间可随时停止。", "color: #647286; font-size: 12px;"));
        noticeLayout->addStretch();
        layout->addWidget(notice);
        layout->addStretch();

        auto *footer = new QHBoxLayout;
        serviceFooter = makeLabel("等待 MCP 调用", "color: #16825d; font-size: 12px;");
        footer->addWidget(serviceFooter);
        footer->addStretch();
        footer->addWidget(makeLabel("远程平台未连接", "color: #929dad; font-size: 12px;"));
        layout->addLayout(footer);
        homePage = content;
        return content;
    }

    QWidget *pageShell(const QString &title, const QString &subtitle, QVBoxLayout **outLayout) {
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

    QWidget *createDevicesPage() {
        QVBoxLayout *layout = nullptr;
        devicesPage = pageShell("我的设备", "设备身份保存在此 Windows 用户的配置目录。", &layout);
        auto *identity = new QFrame;
        identity->setObjectName("identityPanel");
        auto *card = new QVBoxLayout(identity);
        card->setContentsMargins(20, 18, 20, 18);
        card->setSpacing(12);
        card->addWidget(makeLabel("本机设备", "font-size: 15px; font-weight: 650; color: #263446;"));
        deviceIdValue = makeLabel(deviceCode, "font-size: 24px; color: #172435;");
        deviceStatusValue = makeLabel("平台未连接", "font-size: 12px; color: #a45f13;");
        card->addWidget(deviceIdValue);
        card->addWidget(deviceStatusValue);
        auto *row = new QHBoxLayout;
        auto *copy = makeButton("复制设备码");
        auto *forget = makeButton("更换设备码");
        row->addWidget(copy);
        row->addWidget(forget);
        row->addStretch();
        card->addLayout(row);
        connect(copy, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
        connect(forget, &QPushButton::clicked, this, [this] {
            if (QMessageBox::question(this, "更换设备码", "更换后将生成新的设备码。") != QMessageBox::Yes) return;
            const QString path = QDir(qEnvironmentVariable("APPDATA")).filePath("Yiuk/DeviceAssist/device.json");
            QFile::remove(path);
            deviceCode = loadOrCreateDeviceCode();
            refreshDevicesPage();
        });
        layout->addWidget(identity);
        layout->addStretch();
        return devicesPage;
    }

    QWidget *createSessionsPage() {
        QVBoxLayout *layout = nullptr;
        sessionsPage = pageShell("会话记录", "本机记录授权决定和命令任务。记录保存在此客户端的用户配置中。", &layout);
        sessionsHistory = new QLabel;
        sessionsHistory->setAlignment(Qt::AlignTop | Qt::AlignLeft);
        sessionsHistory->setWordWrap(true);
        sessionsHistory->setStyleSheet("color: #435168; background: #f5f7fa; border: 1px solid #edf0f4; border-radius: 7px; padding: 16px;");
        layout->addWidget(sessionsHistory);
        layout->addStretch();
        refreshSessionsPage();
        return sessionsPage;
    }

    QWidget *createSettingsPage() {
        QVBoxLayout *layout = nullptr;
        settingsPage = pageShell("设置", "管理本机协助行为。", &layout);
        auto *security = new QFrame;
        security->setObjectName("identityPanel");
        auto *securityLayout = new QVBoxLayout(security);
        securityLayout->setContentsMargins(18, 16, 18, 16);
        securityLayout->setSpacing(10);
        securityLayout->addWidget(makeLabel("本机协助", "font-size: 15px; font-weight: 650; color: #263446;"));
        acceptingRequests = new QCheckBox("允许本机接收协助请求");
        acceptingRequests->setChecked(QSettings().value("allowLocalDemoRequests", true).toBool());
        acceptingRequests->setStyleSheet("font-size: 13px; color: #435168;");
        securityLayout->addWidget(acceptingRequests);
        auto *risk = makeLabel("PowerShell 将以当前 Windows 用户权限运行。这不是沙箱。", "font-size: 12px; color: #8a5b14; background: #fff6e5; border-radius: 6px; padding: 10px;");
        risk->setWordWrap(true);
        securityLayout->addWidget(risk);
        settingsStatusValue = makeLabel("", "font-size: 12px; color: #69788d;");
        securityLayout->addWidget(settingsStatusValue);
        layout->addWidget(security);
        layout->addSpacing(18);
        layout->addWidget(makeLabel("本地数据", "font-size: 15px; font-weight: 650; color: #263446;"));
        layout->addSpacing(7);
        auto *historyInfo = makeLabel("最多保留 100 条会话和 300 条任务记录，仅保存在当前 Windows 用户配置目录。", "font-size: 12px; color: #69788d;");
        historyInfo->setWordWrap(true);
        layout->addWidget(historyInfo);
        auto *clearHistory = makeButton("清除本地会话和任务记录");
        clearHistory->setObjectName("subtle");
        clearHistory->setStyleSheet("QPushButton { color: #a52b2b; border: 1px solid #e8bcbc; border-radius: 6px; padding: 8px 12px; background: white; } QPushButton:hover { background: #fff0f0; }");
        layout->addWidget(clearHistory, 0, Qt::AlignLeft);
        connect(clearHistory, &QPushButton::clicked, this, [this] {
            QMessageBox confirm(this);
            confirm.setWindowTitle("清除本地记录");
            confirm.setIcon(QMessageBox::Warning);
            confirm.setText("清除所有本机会话和任务记录？");
            confirm.setInformativeText("正在运行的任务不会因此停止。清除后无法恢复。");
            confirm.setStandardButtons(QMessageBox::Yes | QMessageBox::Cancel);
            confirm.setDefaultButton(QMessageBox::Cancel);
            if (confirm.exec() != QMessageBox::Yes) return;
            sessions.clear();
            tasks.clear();
            persistSessionState();
            refreshSessionsPage();
            refreshActiveSessions();
        });
        connect(acceptingRequests, &QCheckBox::toggled, this, [this](bool enabled) {
            if (enabled && !confirmLocalDemoConsent()) {
                acceptingRequests->setChecked(false);
                return;
            }
            QSettings().setValue("allowLocalDemoRequests", enabled);
            refreshSettingsPage();
        });
        layout->addStretch();
        refreshSettingsPage();
        return settingsPage;
    }

    void showPage(int index) {
        pages->setCurrentIndex(index);
        navHome->setChecked(index == 0);
        navDevices->setChecked(index == 1);
        navHistory->setChecked(index == 2);
        navSettings->setChecked(index == 3);
    }

    void refreshDevicesPage() {
        if (deviceIdValue) deviceIdValue->setText(deviceCode);
        if (deviceStatusValue) deviceStatusValue->setText("平台未连接");
    }

    void refreshSessionsPage() {
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

    void refreshSettingsPage() {
        if (settingsStatusValue && acceptingRequests)
            settingsStatusValue->setText(acceptingRequests->isChecked() ? "状态：协助请求已启用" : "状态：协助请求已暂停");
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
        return {{"device_id", deviceId()}, {"name", QSysInfo::machineHostName()},
                {"online", false}, {"local_client_running", bridge && bridge->isListening()},
                {"platform_connected", false}, {"platform", "windows"}, {"backend", "local-demo"}};
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
        serviceState->setText("MCP：本机桥接就绪");
        serviceState->setStyleSheet("font-size: 11px; color: #16825d; padding: 2px 5px;");
        platformState->setText("平台：未连接");
        deviceState->setText("设备：本机未注册");
        serviceFooter->setText("● MCP 已就绪；平台未连接");
    }

    QJsonObject invokeLocalTool(const QString &tool, const QJsonObject &input) {
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
        if (tool == "get_command_result") return commandResult(input);
        if (tool == "cancel_command") return cancelCommand(input);
        if (tool == "close_session") return closeSession(input.value("session_id").toString());
        return fail("UNKNOWN_TOOL", "未支持的工具");
    }

    QJsonObject requestLocalSession(const QJsonObject &input) {
        const QString target = input.value("device_id").toString();
        if (target != deviceId()) return fail("DEVICE_NOT_FOUND", "只能连接当前设备");
        if (!acceptingRequests->isChecked()) return fail("DEVICE_UNAVAILABLE", "本机未开启协助请求；请在设置中启用后重试");
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
        serviceFooter->setText("● 有待批准的协助请求");
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

    QJsonObject sessionStatus(const QString &id) {
        if (!sessions.contains(id)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
        expireSessionIfNeeded(id);
        return ok(sessions.value(id));
    }

    void expireSessionIfNeeded(const QString &id) {
        if (!sessions.contains(id)) return;
        QJsonObject session = sessions.value(id);
        const QString status = session.value("status").toString();
        const qint64 expiresAt = static_cast<qint64>(session.value("expires_at_ms").toDouble());
        if ((status == "pending_approval" || status == "active") && expiresAt > 0 && QDateTime::currentMSecsSinceEpoch() >= expiresAt) {
            session.insert("status", "expired");
            sessions.insert(id, session);
            persistSessionState();
            stopTasksForSession(id, "cancelled");
            refreshPendingRequests();
            refreshActiveSessions();
        }
    }

    void stopTasksForSession(const QString &id, const QString &status) {
        for (auto it = tasks.begin(); it != tasks.end(); ++it) {
            if (it.value().value("session_id").toString() != id || !processes.contains(it.key())) continue;
            QProcess *process = processes.take(it.key());
            process->kill();
            process->deleteLater();
            QFile::remove(taskFiles.take(it.key()));
            QJsonObject task = it.value();
            task.insert("status", status);
            it.value() = task;
        }
    }

    void refreshPendingRequests() {
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
        expireSessionIfNeeded(id);
        if (sessions.value(id).value("status").toString() != "pending_approval") return;
        const QJsonObject session = sessions.value(id);
        QMessageBox dialog(this);
        dialog.setWindowTitle("远程协助授权");
        dialog.setIcon(QMessageBox::Question);
        dialog.setText("Agent 请求在本机执行远程命令");
        const QString permissionText = jsonArrayText(session.value("permissions").toArray());
        dialog.setInformativeText(QString("请求者：%1\n来源设备：%2\n请求时间：%3\n用途：%4\n请求权限：%5\n有效期至：%6\n\n批准后，Agent 可在当前 Windows 用户权限范围内执行多条 PowerShell 命令。普通用户权限不是沙箱。请求者身份尚未经过远程验证。")
            .arg(session.value("requester").toString(), session.value("source_device").toString(),
                 session.value("created_at").toString(), session.value("purpose").toString(),
                 permissionText, session.value("expires_at").toString()));
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
        persistSessionState();
        refreshPendingRequests();
        refreshActiveSessions();
        refreshSessionsPage();
        serviceFooter->setText(approved ? "● 本机会话已批准" : "● 连接申请已拒绝");
    }

    void refreshActiveSessions() {
        while (QLayoutItem *item = activeLayout->takeAt(0)) {
            if (item->widget()) item->widget()->deleteLater();
            delete item;
        }
        bool hasActive = false;
        const QStringList sessionIds = sessions.keys();
        for (const QString &sessionId : sessionIds) {
            expireSessionIfNeeded(sessionId);
            const QJsonObject session = sessions.value(sessionId);
            if (session.value("status").toString() != "active") continue;
            hasActive = true;
            const QString id = sessionId;
            auto *card = new QFrame;
            card->setStyleSheet("QFrame { background: #edf8f2; border: 1px solid #b9e2ca; border-radius: 7px; }");
            auto *column = new QVBoxLayout(card);
            column->setContentsMargins(13, 11, 13, 11);
            auto *titleRow = new QHBoxLayout;
            titleRow->addWidget(makeLabel("活跃会话", "font-size: 14px; font-weight: 650; color: #185c3b;"));
            titleRow->addStretch();
            auto *stop = makeButton("立即停止会话");
            stop->setStyleSheet("QPushButton { color: #a52b2b; border: 1px solid #e8bcbc; background: white; padding: 6px 10px; } QPushButton:hover { background: #fff0f0; }");
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
            connect(stop, &QPushButton::clicked, this, [this, id] {
                if (QMessageBox::question(this, "立即停止会话", "停止后将撤销授权并取消正在运行的任务。") == QMessageBox::Yes)
                    closeSession(id);
            });
            activeLayout->addWidget(card);
        }
        activePanel->setVisible(hasActive);
    }

    QString activeTaskSummary(const QString &sessionId) const {
        for (auto it = tasks.cbegin(); it != tasks.cend(); ++it) {
            if (it.value().value("session_id").toString() == sessionId && it.value().value("status").toString() == "running")
                return "PowerShell 正在运行 (" + it.key().left(8) + ")";
        }
        return "无运行任务";
    }

    QJsonObject executeLocalCommand(const QJsonObject &input) {
        const QString sessionId = input.value("session_id").toString();
        expireSessionIfNeeded(sessionId);
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
            persistSessionState();
            QFile::remove(taskFiles.take(requestId));
            processes.remove(requestId);
            process->deleteLater();
            refreshSessionsPage();
            refreshActiveSessions();
            serviceFooter->setText("● PowerShell 任务完成");
        });
        connect(process, &QProcess::errorOccurred, this, [this, requestId, process](QProcess::ProcessError) {
            if (process->state() != QProcess::NotRunning) return;
            QJsonObject task = tasks.value(requestId);
            task.insert("status", "failed");
            task.insert("stderr", task.value("stderr").toString() + process->errorString());
            tasks.insert(requestId, task);
            persistSessionState();
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
            persistSessionState();
            process->kill();
            QFile::remove(taskFiles.take(requestId));
            processes.remove(requestId);
        });
        serviceFooter->setText("● 正在执行本机 PowerShell 任务");
        refreshActiveSessions();
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
        persistSessionState();
        refreshSessionsPage();
        return ok(QJsonObject{{"request_id", requestId}, {"status", "cancelled"}});
    }

    QJsonObject closeSession(const QString &id) {
        if (!sessions.contains(id)) return fail("SESSION_EXPIRED", "会话不存在或已过期");
        QJsonObject session = sessions.value(id);
        session.insert("status", "closed");
        sessions.insert(id, session);
        persistSessionState();
        stopTasksForSession(id, "cancelled");
        refreshPendingRequests();
        refreshActiveSessions();
        refreshSessionsPage();
        notificationSessions.remove(id);
        serviceFooter->setText("● 本机会话已关闭");
        return ok(session);
    }

    void copyToClipboard(const QString &text, const QString &message) {
        QApplication::clipboard()->setText(text);
        statusBar()->showMessage(message, 1800);
    }

};

} // namespace

int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    app.setStyle("Fusion");
    QPalette palette;
    palette.setColor(QPalette::Window, QColor("#ffffff"));
    palette.setColor(QPalette::WindowText, QColor("#182230"));
    palette.setColor(QPalette::Base, QColor("#ffffff"));
    palette.setColor(QPalette::AlternateBase, QColor("#f5f7fa"));
    palette.setColor(QPalette::Text, QColor("#182230"));
    palette.setColor(QPalette::Button, QColor("#ffffff"));
    palette.setColor(QPalette::ButtonText, QColor("#263446"));
    palette.setColor(QPalette::Highlight, QColor("#1479ed"));
    palette.setColor(QPalette::HighlightedText, QColor("#ffffff"));
    app.setPalette(palette);
    app.setApplicationName("Yiuk Device Assist");
    app.setOrganizationName("Yiuk");
    MainWindow window;
    window.show();
    return app.exec();
}
