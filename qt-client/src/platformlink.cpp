#include "mainwindow.h"

#include "yiukdialog.h"


void MainWindow::startLocalBridge() {
    bridge = new LocalBridge([this](const QString &tool, const QJsonObject &input) {
        return invokeLocalTool(tool, input);
    }, this);
    QString error;
    if (!bridge->start(&error)) {
        serviceState->setText("● 本机 MCP 桥接启动失败");
        serviceState->setStyleSheet("font-size: 11px; color: #b42318; padding: 0 5px;");
        return;
    }
    const QString configRoot = yiukConfigRoot();
    if (!QDir().mkpath(configRoot)) {
        serviceState->setText("● MCP 配置目录创建失败");
        return;
    }
    QFile config(QDir(configRoot).filePath("local-api.json"));
    if (!config.open(QIODevice::WriteOnly | QIODevice::Truncate)) {
        serviceState->setText("● MCP 配置写入失败");
        return;
    }
    const QJsonObject configObject{{"url", bridge->endpoint()}, {"token", bridge->token()}};
    if (config.write(QJsonDocument(configObject).toJson(QJsonDocument::Compact)) < 0) {
        serviceState->setText("● MCP 配置写入失败");
        return;
    }
    config.close();
    serviceState->setText("MCP：本机桥接就绪");
    serviceState->setStyleSheet("font-size: 11px; color: #16825d; padding: 2px 5px;");
    platformState->setText("平台：未连接");
    deviceState->setText("设备：本机未注册");
}


QString MainWindow::platformConfigPath() const {
    return yiukConfigPath("platform.json");
}


void MainWindow::loadPlatformConfig() {
    PlatformClient::Config cfg;
    cfg.deviceId = deviceId();
    cfg.deviceName = QSysInfo::machineHostName();
    cfg.mode = "approval";
    cfg.ttlSeconds = 24 * 60 * 60;
    cfg.enabled = true;
    cfg.allowInsecure = false;
    cfg.temporaryPassword = randomPassword(32);
    QFile file(findYiukConfigPath("platform.json"));
    if (file.open(QIODevice::ReadOnly)) {
        const QJsonObject object = QJsonDocument::fromJson(file.readAll()).object();
        cfg.enabled = true;
        cfg.address = object.value("address").toString();
        cfg.tls = object.value("tls").toBool(true);
        cfg.allowInsecure = false;
        cfg.caFile = object.value("ca_file").toString();
        cfg.pinnedFingerprint = object.value("certificate_fingerprint").toString();
        cfg.mode = object.value("mode").toString("approval");
        cfg.passwordVersion = object.value("password_version").toInt(1);
        cfg.playerPassword = SecureStore::unprotect(object.value("player_password").toString());
        const QString storedPassword = SecureStore::unprotect(object.value("temporary_password").toString());
        if (!storedPassword.isEmpty()) cfg.temporaryPassword = storedPassword;
    }
    temporaryPassword = cfg.temporaryPassword;
    passwordVersion = cfg.passwordVersion;
    platform->setConfig(cfg);
    const QSignalBlocker blockAddress(platformAddress);
    const QSignalBlocker blockPassword(platformPassword);
    const QSignalBlocker blockTls(platformTls);
    const QSignalBlocker blockMode(platformMode);
    if (platformAddress) platformAddress->setText(cfg.address);
    if (platformPassword) platformPassword->setText(cfg.playerPassword);
    if (platformTls) platformTls->setChecked(cfg.tls);
    if (platformMode) {
        const int index = platformMode->findData(cfg.mode);
        platformMode->setCurrentIndex(index >= 0 ? index : 0);
    }
    if (passwordValue) passwordValue->setText(passwordVisible ? groupedPassword(temporaryPassword) : "********");
    syncMcpClientConfig(cfg);
}


void MainWindow::savePlatformConfig() {
    if (!platform) return;
    PlatformClient::Config cfg = platform->config();
    cfg.deviceId = deviceId();
    cfg.deviceName = QSysInfo::machineHostName();
    cfg.temporaryPassword = temporaryPassword;
    cfg.passwordVersion = passwordVersion;
    if (platformAddress) cfg.address = platformAddress->text().trimmed();
    if (platformPassword) cfg.playerPassword = platformPassword->text();
    if (platformTls) cfg.tls = platformTls->isChecked();
    cfg.enabled = true;
    cfg.allowInsecure = false;
    if (platformMode) cfg.mode = platformMode->currentData().toString();
    platform->setConfig(cfg);
    QDir().mkpath(QFileInfo(platformConfigPath()).absolutePath());
    const QJsonObject object{
        {"enabled", true},
        {"address", cfg.address},
        {"tls", cfg.tls},
        {"allow_insecure", false},
        {"ca_file", cfg.caFile},
        {"certificate_fingerprint", cfg.pinnedFingerprint},
        {"mode", cfg.mode},
        {"password_version", cfg.passwordVersion},
        {"player_password", SecureStore::protect(cfg.playerPassword)},
        {"temporary_password", SecureStore::protect(cfg.temporaryPassword)},
    };
    QFile file(platformConfigPath());
    if (file.open(QIODevice::WriteOnly | QIODevice::Truncate))
        file.write(QJsonDocument(object).toJson(QJsonDocument::Compact));
    syncMcpClientConfig(cfg);
}


QString MainWindow::mcpToolSourcePath() const {
    return QDir(QCoreApplication::applicationDirPath()).filePath("yiuk-mcp.exe");
}


QString MainWindow::mcpAgentConfigText() const {
    QJsonObject server{{"command", mcpToolSourcePath()}, {"args", QJsonArray{}}};
    QJsonObject root{{"mcpServers", QJsonObject{{"yiuk-switchboard", server}}}};
    return QString::fromUtf8(QJsonDocument(root).toJson(QJsonDocument::Indented));
}


bool MainWindow::syncMcpClientConfig(const PlatformClient::Config &cfg) {
    if (cfg.address.trimmed().isEmpty() || cfg.playerPassword.isEmpty()) return false;
    const QString root = yiukConfigRoot();
    if (!QDir().mkpath(root)) return false;
    QJsonObject object{
        {"address", cfg.address.trimmed()},
        {"player_password", cfg.playerPassword},
        {"timeout_seconds", 30},
    };
    if (cfg.tls) {
        if (!cfg.caFile.trimmed().isEmpty()) object.insert("tls_ca", cfg.caFile.trimmed());
        if (!cfg.pinnedFingerprint.trimmed().isEmpty()) object.insert("pinned_cert_sha256", cfg.pinnedFingerprint.trimmed());
        object.insert("insecure_skip_verify", cfg.allowInsecure);
    } else {
        object.insert("allow_plaintext", true);
    }
    QFile file(QDir(root).filePath("platform-client.json"));
    if (!file.open(QIODevice::WriteOnly | QIODevice::Truncate)) return false;
    return file.write(QJsonDocument(object).toJson(QJsonDocument::Compact)) >= 0;
}


void MainWindow::copyMcpAgentConfig() {
    copyToClipboard(mcpAgentConfigText(), "Agent MCP 配置已复制");
    showStatusMessage("把配置粘贴到 Agent 的 MCP 设置中即可", 4000);
}


void MainWindow::startPlatform() {
    platform = new PlatformClient(this);
    connect(platform, &PlatformClient::stateChanged, this, [this](const QString &state) { updatePlatformState(state); });
    connect(platform, &PlatformClient::credentialsRenewed, this,
            [this](const QString &password, int version) {
        temporaryPassword = password;
        passwordVersion = version;
        if (passwordValue)
            passwordValue->setText(passwordVisible ? groupedPassword(password) : "********");
        savePlatformConfig();
    });
    connect(platform, &PlatformClient::deviceListReceived, this, &MainWindow::onDeviceListReceived);
    connect(platform, &PlatformClient::requestFailed, this, &MainWindow::onPlatformRequestFailed);
    connect(platform, &PlatformClient::sessionRequested, this, &MainWindow::onPlatformSessionRequested);
    connect(platform, &PlatformClient::sessionUpdated, this, &MainWindow::onPlatformSessionUpdated);
    connect(platform, &PlatformClient::sessionCreated, this, &MainWindow::onPlatformSessionCreated);
    connect(platform, &PlatformClient::commandRequested, this, &MainWindow::onPlatformCommandRequested);
    connect(platform, &PlatformClient::fileDownloadRequested, this, &MainWindow::onPlatformFileDownloadRequested);
    connect(platform, &PlatformClient::fileDownloadCanceled, this, [this](const QString &sessionId, const QString &requestId) {
        activeFileDownloads.remove(requestId);
        appendActivity(sessionId, QString("平台已取消文件下载 %1").arg(requestId.left(8)),
                       ActivityKind::Download);
    });
    connect(platform, &PlatformClient::cancelRequested, this, &MainWindow::onPlatformCancelRequested);
    connect(platform, &PlatformClient::closeRequested, this, &MainWindow::onPlatformCloseRequested);
    connect(platform, &PlatformClient::temporaryPasswordUpdated, this,
            [this](const QString &password, int version, qint64) {
        temporaryPassword = password;
        passwordVersion = version;
        if (passwordValue)
            passwordValue->setText(passwordVisible ? groupedPassword(temporaryPassword) : "********");
        savePlatformConfig();
        showStatusMessage("连接密码已更新", 1800);
    });
    connect(platform, &PlatformClient::platformError, this, [this](const QString &code, const QString &message) {
        showStatusMessage(QString("平台错误：%1 %2").arg(code, message), 4000);
    });
    connect(platform, &PlatformClient::connectionError, this, [this](const QString &message) {
        showStatusMessage("平台连接：" + message, 4000);
    });
    connect(platform, &PlatformClient::certificateUntrusted, this,
            [this](const QString &fingerprint, const QString &subject, const QString &details, bool changed) {
        // Frameless security prompt on the shared white sheet; the amber banner
        // reuses the warning style the settings page already uses.
        YiukDialog dialog(this, changed ? QStringLiteral("服务器证书已变更") : QStringLiteral("服务器证书不受信任"));
        dialog.setFixedWidth(560);
        auto *banner = makeLabel(changed ? QStringLiteral("服务器证书与之前记录的不一致，可能存在中间人攻击。")
                                         : QStringLiteral("服务器使用了自签名证书，系统无法验证其身份。"),
                                 "font-size: 12px; color: #8a5b14; background: #fff6e5; "
                                 "border: 1px solid #f3dfb0; border-radius: 6px; padding: 7px 9px;");
        banner->setWordWrap(true);
        dialog.content()->addWidget(banner);
        auto *detailsLabel = makeLabel(QString("证书名称：%1\nSHA-256 指纹：%2\n\n%3\n\n仅当你能确认这是自己的服务器时，才选择“信任此证书”。")
            .arg(subject, fingerprint, details),
            "font-size: 12px; color: #49566a;");
        detailsLabel->setTextFormat(Qt::PlainText);
        detailsLabel->setWordWrap(true);
        detailsLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
        dialog.content()->addWidget(detailsLabel);
        dialog.content()->addStretch();
        auto *cancel = dialog.addButton(QStringLiteral("取消"));
        auto *trust = dialog.addButton(QStringLiteral("信任此证书"), YiukDialog::ButtonKind::Primary);
        (changed ? cancel : trust)->setDefault(true);
        QObject::connect(cancel, &QPushButton::clicked, &dialog, &QDialog::reject);
        QObject::connect(trust, &QPushButton::clicked, &dialog, &QDialog::accept);
        dialog.adjustSize();
        if (dialog.exec() != QDialog::Accepted) {
            showStatusMessage("已取消连接：服务器证书未被信任", 5000);
            return;
        }
        PlatformClient::Config cfg = platform->config();
        cfg.pinnedFingerprint = fingerprint;
        platform->setConfig(cfg);
        savePlatformConfig();
        platform->connectToPlatform();
    });
    loadPlatformConfig();
    if (!platform->config().address.isEmpty()) {
        showStatusMessage("启动后自动连接平台…", 3000);
        // 等窗口首帧渲染后再连接，避免启动时 TLS 握手与证书弹框抢在界面出现前。
        QTimer::singleShot(0, this, [this] {
            if (platform && platform->state() == "disconnected") platform->connectToPlatform();
        });
    }
    updatePlatformState(platform->state());
}


void MainWindow::onDeviceListReceived(const QJsonArray &devices, const QString &requestId) {
    if (pendingDeviceLookupId.isEmpty() || requestId != pendingDeviceLookupId) return;
    const QString deviceId = pendingDeviceLookupCode;
    pendingDeviceLookupId.clear();
    pendingDeviceLookupCode.clear();
    QJsonObject found;
    for (const QJsonValue &value : devices) {
        const QJsonObject device = value.toObject();
        if (device.value("device_id").toString() == deviceId) {
            found = device;
            break;
        }
    }
    if (found.isEmpty()) {
        showWarningMessage(this, "设备不存在",
            "后端没有找到设备码 " + spacedCode(deviceId) + "，请确认对方设备已在平台注册。");
        return;
    }
    if (!found.value("online").toBool()) {
        showWarningMessage(this, "设备不在线",
            "后端找到了设备码 " + spacedCode(deviceId) + "，但该设备当前不在线。");
        return;
    }
    promptAndRequestConnection(deviceId);
}


void MainWindow::onPlatformRequestFailed(const QString &requestId, const QString &code, const QString &message) {
    if (pendingDeviceLookupId.isEmpty() || requestId != pendingDeviceLookupId) return;
    pendingDeviceLookupId.clear();
    pendingDeviceLookupCode.clear();
    showWarningMessage(this, "设备查询失败",
        QString("后端返回错误：%1\n%2").arg(code, message));
}


void MainWindow::updatePlatformState(const QString &state) {
    const bool connected = state == "connected";
    const bool connecting = state == "connecting" || state == "authenticating";
    const bool linkLost = platformLinkWasUp && !connected;
    platformLinkWasUp = connected;
    if (linkLost) {
        // The link is gone, so no platform session can still be running.
        // Without this the owner UI keeps showing "远程协助进行中" after the
        // platform already dropped the connection.
        interruptPlatformSessions();
    }
    if (platformState) {
        platformState->setText(connected ? "平台：已连接" : (connecting ? "平台：连接中" : "平台：未连接"));
        platformState->setStyleSheet(connected ? "font-size: 11px; color: #16825d; padding: 2px 5px;"
                                               : "font-size: 11px; color: #687588; padding: 2px 5px;");
    }
    if (deviceState) deviceState->setText(connected ? "设备：平台在线" : "设备：未注册");
    if (platformStatusValue) platformStatusValue->setText(connected ? "状态：平台已连接" : (connecting ? "状态：正在连接平台…" : "状态：平台未连接"));
    if (platformConnectButton) platformConnectButton->setText(connected || connecting ? "断开平台" : "连接平台");
    refreshDevicesPage();
}
