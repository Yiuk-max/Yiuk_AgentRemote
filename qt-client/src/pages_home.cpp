#include "mainwindow.h"

#include "yiukdialog.h"


QWidget *MainWindow::createHomePage() {
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
    passwordRow->setContentsMargins(0, 0, 0, 0);
    passwordValue = makeLabel("********", "font-family: Consolas, monospace; font-size: 13px; color: #172435;");
    passwordValue->setMinimumHeight(32);
    passwordValue->setAlignment(Qt::AlignLeft | Qt::AlignVCenter);
    passwordRow->addWidget(passwordValue, 1);
    passwordColumn->addLayout(passwordRow);
    auto *passwordActions = new QHBoxLayout;
    passwordActions->setContentsMargins(0, 0, 0, 0);
    passwordActions->addStretch();
    auto *copyPassword = iconButton(Glyph::Copy, "复制密码");
    togglePassword = iconButton(Glyph::EyeOff, "显示密码");
    togglePassword->setIcon(QIcon(QStringLiteral(":/icons/eye-closed.svg")));
    togglePassword->setObjectName("passwordEye");
    togglePassword->setCheckable(true);
    auto *refreshPassword = iconButton(Glyph::Refresh, "重新生成密码");
    passwordActions->addWidget(copyPassword);
    passwordActions->addWidget(togglePassword);
    passwordActions->addWidget(refreshPassword);
    passwordColumn->addLayout(passwordActions);
    identityLayout->addLayout(passwordColumn, 1);
    layout->addWidget(identity);

    connect(copyCode, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
    connect(togglePassword, &QPushButton::toggled, this, [this](bool visible) {
        passwordVisible = visible;
        passwordValue->setText(passwordVisible ? formatTemporaryPassword(temporaryPassword) : "********");
        togglePassword->setIcon(QIcon(passwordVisible ? QStringLiteral(":/icons/eye-open.svg")
                                                       : QStringLiteral(":/icons/eye-closed.svg")));
        togglePassword->setToolTip(passwordVisible ? "隐藏密码" : "显示密码");
    });
    connect(copyPassword, &QPushButton::clicked, this, [this] { copyToClipboard(temporaryPassword, "密码已复制"); });
    connect(refreshPassword, &QPushButton::clicked, this, [this] {
        if (!platform || !platform->isConnected()) {
            showStatusMessage("请先连接平台，再手动更新连接密码", 2500);
            return;
        }
        temporaryPassword = generateTemporaryPassword(32);
        ++passwordVersion;
        const QString mode = platform->config().mode.isEmpty() ? QStringLiteral("approval") : platform->config().mode;
        platform->updateTemporaryPassword(temporaryPassword, passwordVersion, mode, platform->config().ttlSeconds);
        showStatusMessage("正在手动更新连接密码", 1800);
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

    layout->addStretch();
    homePage = content;
    return content;
}


void MainWindow::lookupDeviceBeforeConnecting(const QString &deviceId) {
    if (!platform || !platform->isConnected()) {
        showInfoMessage(this, "远程平台未连接", "设备码：" + formatDeviceCode(deviceId) + "\n\n请先在设置中连接平台服务器。");
        return;
    }
    if (!pendingDeviceLookupCorrelationId.isEmpty()) return;
    pendingDeviceLookupCorrelationId = QUuid::createUuid().toString(QUuid::WithoutBraces);
    pendingDeviceLookupDeviceId = deviceId;
    platform->listDevices(pendingDeviceLookupCorrelationId);
    showStatusMessage("正在向后端查询设备…", 2000);
    const QString requestId = pendingDeviceLookupCorrelationId;
    QTimer::singleShot(6000, this, [this, requestId] {
        if (pendingDeviceLookupCorrelationId != requestId) return;
        pendingDeviceLookupCorrelationId.clear();
        pendingDeviceLookupDeviceId.clear();
        showWarningMessage(this, "设备查询超时", "后端没有响应，请稍后重试。");
    });
}


void MainWindow::promptAndRequestConnection(const QString &deviceId) {
    if (hasOtherLiveSession()) {
        showWarningMessage(this, "已有活动会话", "当前客户端已有一个活动或待批准会话，请先结束它再发起新的连接。");
        return;
    }
    if (!platform || !platform->isConnected()) return;
    QString temporary;
    if (!askText(this, "连接设备",
                 "输入设备所有者提供的临时密码可直接连接；留空则发送申请并等待对方批准：",
                 QLineEdit::Password, QString(), &temporary))
        return;
    if (!temporary.isEmpty() && temporary.size() < 32) {
        showWarningMessage(this, "临时密码无效", "临时密码至少需要 32 个字符。");
        return;
    }
    QString purpose;
    if (!askText(this, "协助用途", "简要说明本次协助用途：", QLineEdit::Normal,
                 QStringLiteral("远程协助"), &purpose) || purpose.isEmpty())
        return;
    platform->requestSession(deviceId, purpose, QStringList{QStringLiteral("execute_command")}, temporary);
    showStatusMessage(temporary.isEmpty() ? "已发送连接申请，等待对方批准" : "正在建立直接连接", 4000);
}
