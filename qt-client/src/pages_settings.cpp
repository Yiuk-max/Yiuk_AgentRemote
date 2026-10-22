#include "mainwindow.h"


QWidget *MainWindow::createSettingsPage() {
    QVBoxLayout *layout = nullptr;
    settingsPage = pageShell("设置", "管理本机协助、平台连接和本地数据。", &layout);

    auto *settingsBody = new QHBoxLayout;
    settingsBody->setContentsMargins(0, 0, 0, 0);
    settingsBody->setSpacing(16);

    auto *settingsNav = new QFrame;
    settingsNav->setObjectName("settingsNav");
    settingsNav->setFixedWidth(148);
    auto *settingsNavLayout = new QVBoxLayout(settingsNav);
    settingsNavLayout->setContentsMargins(8, 12, 8, 12);
    auto *settingsCategoryGroup = new QButtonGroup(settingsNav);
    settingsCategoryGroup->setExclusive(true);
    settingsNavLayout->setSpacing(3);
    settingsNavLayout->addWidget(makeLabel("设置项目", "font-size: 12px; font-weight: 650; color: #8793a3; padding: 0 8px 8px;"));

    auto *generalCategory = new QPushButton("本机协助");
    auto *platformCategory = new QPushButton("平台服务");
    auto *dataCategory = new QPushButton("本地数据");
    const QList<QPushButton *> categories{generalCategory, platformCategory, dataCategory};
    for (QPushButton *category : categories) {
        settingsCategoryGroup->addButton(category);
        category->setObjectName("settingsCategory");
        category->setCheckable(true);
        category->setCursor(Qt::PointingHandCursor);
        category->setMinimumHeight(38);
        settingsNavLayout->addWidget(category);
    }
    generalCategory->setChecked(true);
    settingsNavLayout->addStretch();
    settingsBody->addWidget(settingsNav);

    auto *settingsScroll = new QScrollArea;
    settingsScroll->setObjectName("settingsScroll");
    settingsScroll->setWidgetResizable(true);
    settingsScroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    settingsScroll->setVerticalScrollBarPolicy(Qt::ScrollBarAsNeeded);
    auto *settingsContent = new QWidget;
    auto *settingsContentLayout = new QVBoxLayout(settingsContent);
    settingsContentLayout->setContentsMargins(0, 0, 8, 16);
    settingsContentLayout->setSpacing(14);
    settingsScroll->setWidget(settingsContent);
    settingsBody->addWidget(settingsScroll, 1);
    layout->addLayout(settingsBody, 1);

    auto addSectionHeader = [](QVBoxLayout *sectionLayout, const QString &title, const QString &description) {
        sectionLayout->addWidget(makeLabel(title, "font-size: 16px; font-weight: 650; color: #263446;"));
        if (!description.isEmpty()) {
            auto *descriptionLabel = makeLabel(description, "font-size: 12px; color: #7a8798;");
            descriptionLabel->setWordWrap(true);
            sectionLayout->addWidget(descriptionLabel);
        }
    };
    auto addFormRow = [](QVBoxLayout *sectionLayout, const QString &labelText, QWidget *control) {
        auto *row = new QHBoxLayout;
        row->setContentsMargins(0, 0, 0, 0);
        row->setSpacing(14);
        auto *label = makeLabel(labelText);
        label->setObjectName("settingsFieldLabel");
        label->setFixedWidth(96);
        label->setAlignment(Qt::AlignLeft | Qt::AlignVCenter);
        row->addWidget(label);
        row->addWidget(control, 1);
        sectionLayout->addLayout(row);
    };
    auto makeSettingsCard = []() {
        auto *card = new QFrame;
        card->setObjectName("settingsCard");
        auto *cardLayout = new QVBoxLayout(card);
        cardLayout->setContentsMargins(18, 16, 18, 16);
        cardLayout->setSpacing(11);
        return qMakePair(card, cardLayout);
    };

    auto generalParts = makeSettingsCard();
    QFrame *generalSection = generalParts.first;
    QVBoxLayout *generalLayout = generalParts.second;
    addSectionHeader(generalLayout, "本机协助", "控制此电脑是否接受来自本地工具的协助请求。");
    acceptingRequests = new QCheckBox("允许本机接收协助请求");
    acceptingRequests->setStyleSheet("font-size: 13px; color: #435168;");
    acceptingRequests->setChecked(QSettings().value("allowLocalDemoRequests", true).toBool());
    generalLayout->addWidget(acceptingRequests);
    auto *risk = makeLabel("PowerShell 将以当前 Windows 用户权限运行。这不是沙箱。", "font-size: 12px; color: #8a5b14; background: #fff6e5; border: 1px solid #f3dfb0; border-radius: 6px; padding: 9px;");
    risk->setWordWrap(true);
    generalLayout->addWidget(risk);
    settingsStatusValue = makeLabel("", "font-size: 12px; color: #69788d;");
    generalLayout->addWidget(settingsStatusValue);
    settingsContentLayout->addWidget(generalSection);

    auto platformParts = makeSettingsCard();
    QFrame *platformSection = platformParts.first;
    QVBoxLayout *platformLayout = platformParts.second;
    addSectionHeader(platformLayout, "平台服务", "");
    platformAddress = new QLineEdit;
    platformAddress->setObjectName("settingsField");
    platformAddress->setMinimumHeight(36);
    platformAddress->setPlaceholderText("switchboard.example.com:25664");
    addFormRow(platformLayout, "服务器地址", platformAddress);

    platformPassword = new QLineEdit;
    platformPassword->setObjectName("settingsField");
    platformPassword->setMinimumHeight(36);
    platformPassword->setEchoMode(QLineEdit::Password);
    addFormRow(platformLayout, "Player 密码", platformPassword);

    platformTls = new QCheckBox("使用 TLS");
    platformTls->setChecked(true);
    addFormRow(platformLayout, "连接安全", platformTls);

    platformMode = new QComboBox;
    platformMode->setObjectName("settingsField");
    platformMode->setMinimumHeight(36);
    platformMode->addItem("等待对方同意", "approval");
    platformMode->addItem("直接远程", "direct");
    addFormRow(platformLayout, "授权模式", platformMode);

    auto *platformActionRow = new QHBoxLayout;
    platformActionRow->setContentsMargins(110, 3, 0, 0);
    platformActionRow->setSpacing(12);
    platformConnectButton = makeButton("连接平台");
    platformConnectButton->setObjectName("primary");
    platformConnectButton->setMinimumHeight(36);
    platformActionRow->addWidget(platformConnectButton);
    platformStatusValue = makeLabel("状态：平台未连接", "font-size: 12px; color: #69788d;");
    platformActionRow->addWidget(platformStatusValue);
    platformActionRow->addStretch();
    platformLayout->addLayout(platformActionRow);
    settingsContentLayout->addWidget(platformSection);

    auto agentParts = makeSettingsCard();
    QFrame *agentSection = agentParts.first;
    QVBoxLayout *agentLayout = agentParts.second;
    addSectionHeader(agentLayout, "Agent 集成", "");
    auto *agentActionRow = new QHBoxLayout;
    auto *copyMcpConfig = makeButton("复制 Agent 配置");
    copyMcpConfig->setObjectName("primary");
    agentActionRow->addWidget(copyMcpConfig);
    agentActionRow->addStretch();
    agentLayout->addLayout(agentActionRow);
    settingsContentLayout->addWidget(agentSection);
    connect(copyMcpConfig, &QPushButton::clicked, this, [this] { copyMcpAgentConfig(); });

    auto dataParts = makeSettingsCard();
    QFrame *dataSection = dataParts.first;
    QVBoxLayout *dataLayout = dataParts.second;
    addSectionHeader(dataLayout, "本地数据", "会话和任务记录仅保存在当前 Windows 用户配置目录。");
    auto *historyInfo = makeLabel("最多保留 100 条会话和 300 条任务记录。清除记录不会停止正在运行的任务。", "font-size: 12px; color: #69788d;");
    historyInfo->setWordWrap(true);
    dataLayout->addWidget(historyInfo);
    auto *clearHistory = makeButton("清除本地会话和任务记录");
    clearHistory->setObjectName("subtle");
    clearHistory->setStyleSheet("QPushButton { color: #a52b2b; border: 1px solid #e8bcbc; border-radius: 6px; padding: 8px 12px; background: white; } QPushButton:hover { background: #fff0f0; }");
    dataLayout->addWidget(clearHistory, 0, Qt::AlignLeft);
    settingsContentLayout->addWidget(dataSection);
    settingsContentLayout->addStretch();

    connect(generalCategory, &QPushButton::clicked, this, [settingsScroll, generalSection] {
        settingsScroll->ensureWidgetVisible(generalSection);
    });
    connect(platformCategory, &QPushButton::clicked, this, [settingsScroll, platformSection] {
        settingsScroll->ensureWidgetVisible(platformSection);
    });
    connect(dataCategory, &QPushButton::clicked, this, [settingsScroll, dataSection] {
        settingsScroll->ensureWidgetVisible(dataSection);
    });

    connect(platformConnectButton, &QPushButton::clicked, this, [this] {
        if (!platform) return;
        savePlatformConfig();
        const QString state = platform->state();
        if (platform->isConnected() || state == "connecting" || state == "authenticating")
            platform->disconnectFromPlatform();
        else
            platform->connectToPlatform();
        updatePlatformState(platform->state());
    });
    connect(platformAddress, &QLineEdit::editingFinished, this, [this] {
        if (platform && platformAddress) {
            PlatformClient::Config cfg = platform->config();
            const QString address = platformAddress->text().trimmed();
            if (cfg.address.trimmed() != address) {
                // A different server is a different identity; never reuse
                // the previous certificate trust for it.
                cfg.pinnedFingerprint.clear();
                platform->setConfig(cfg);
            }
        }
        savePlatformConfig();
    });
    connect(platformPassword, &QLineEdit::editingFinished, this, [this] { savePlatformConfig(); });
    connect(platformTls, &QCheckBox::toggled, this, [this] { savePlatformConfig(); });
    connect(platformMode, qOverload<int>(&QComboBox::currentIndexChanged), this, [this](int) {
        savePlatformConfig();
        if (platform && platform->isConnected() && platformMode) {
            platform->updateTemporaryPassword(temporaryPassword, passwordVersion, platformMode->currentData().toString(), platform->config().ttlSeconds);
            statusBar()->showMessage("授权模式已同步到平台", 1800);
        }
    });
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
    refreshSettingsPage();
    return settingsPage;
}
