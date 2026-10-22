#include "mainwindow.h"


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
    togglePassword = iconButton(Glyph::Eye, "显示密码");
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
        passwordValue->setText(passwordVisible ? groupedPassword(temporaryPassword) : "********");
        togglePassword->setIcon(glyphIcon(passwordVisible ? Glyph::EyeOff : Glyph::Eye));
        togglePassword->setToolTip(passwordVisible ? "隐藏密码" : "显示密码");
    });
    connect(copyPassword, &QPushButton::clicked, this, [this] { copyToClipboard(temporaryPassword, "密码已复制"); });
    connect(refreshPassword, &QPushButton::clicked, this, [this] {
        if (!platform || !platform->isConnected()) {
            statusBar()->showMessage("请先连接平台，再手动更新连接密码", 2500);
            return;
        }
        temporaryPassword = randomPassword(32);
        ++passwordVersion;
        const QString mode = platformMode ? platformMode->currentData().toString() : QStringLiteral("approval");
        platform->updateTemporaryPassword(temporaryPassword, passwordVersion, mode, platform->config().ttlSeconds);
        statusBar()->showMessage("正在手动更新连接密码", 1800);
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
    remoteCode->setPlaceholderText("例如 123 456 789");
    remoteCode->setMaxLength(11);
    fieldLayout->addWidget(remoteCode, 1);
    auto *favoriteButton = iconButton(Glyph::Star, "添加设备");
    favoriteButton->setFixedSize(38, 38);
    favoriteButton->setIconSize(QSize(22, 22));
    fieldLayout->addWidget(favoriteButton);
    connectRow->addWidget(field, 1);
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
    connect(favoriteButton, &QPushButton::clicked, this, [this] {
        QString digits;
        for (const QChar ch : remoteCode->text()) if (ch.isDigit()) digits += ch;
        if (digits.size() != 9) {
            QMessageBox::warning(this, "设备码无效", "请输入完整的 9 位设备码后添加。");
            return;
        }
        const QString formatted = spacedCode(digits);
        if (!savedDevices.contains(formatted)) {
            savedDevices.append(formatted);
            QSettings().setValue("savedDevices", savedDevices);
            refreshDevicesPage();
            statusBar()->showMessage("设备已添加", 1800);
        } else {
            statusBar()->showMessage("设备已在列表中", 1800);
        }
    });
    connect(connectButton, &QPushButton::clicked, this, [this] {
        QString digits;
        for (const QChar ch : remoteCode->text()) if (ch.isDigit()) digits += ch;
        if (digits.size() != 9) {
            QMessageBox::warning(this, "设备码无效", "请输入完整的 9 位设备码。");
            return;
        }
        if (!platform || !platform->isConnected()) {
            QMessageBox::information(this, "远程平台未连接", "设备码：" + spacedCode(digits) + "\n\n请先在设置中连接平台服务器。");
            return;
        }
        bool passwordOk = false;
        const QString temporary = QInputDialog::getText(this, "输入临时密码",
            "请输入对方设备所有者在安全渠道提供的 32 位临时密码：", QLineEdit::Password, QString(), &passwordOk).trimmed();
        if (!passwordOk) return;
        if (temporary.size() < 32) {
            QMessageBox::warning(this, "临时密码无效", "临时密码至少需要 32 个字符。");
            return;
        }
        bool purposeOk = false;
        const QString purpose = QInputDialog::getText(this, "协助用途",
            "简要说明本次协助用途：", QLineEdit::Normal, QStringLiteral("远程协助"), &purposeOk).trimmed();
        if (!purposeOk || purpose.isEmpty()) return;
        platform->requestSession(digits, purpose, QStringList{QStringLiteral("execute_command")}, temporary);
        statusBar()->showMessage("已发送协助请求，等待对方批准", 4000);
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
