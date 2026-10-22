#include "mainwindow.h"

#include "yiukdialog.h"


QWidget *MainWindow::createSettingsPage() {
    QVBoxLayout *layout = nullptr;
    settingsPage = pageShell("设置", "管理本机协助、平台连接和本地数据。", &layout);
    layout->setContentsMargins(57, 16, 57, 10);

    auto *settingsBody = new QVBoxLayout;
    settingsBody->setContentsMargins(0, 0, 0, 0);
    settingsBody->setSpacing(0);

    auto *settingsScroll = new QScrollArea;
    settingsScroll->setObjectName("settingsScroll");
    settingsScroll->setWidgetResizable(true);
    settingsScroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    settingsScroll->setVerticalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    auto *settingsContent = new QWidget;
    auto *settingsContentLayout = new QVBoxLayout(settingsContent);
    settingsContentLayout->setContentsMargins(0, 0, 0, 8);
    settingsContentLayout->setSpacing(8);
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
        cardLayout->setContentsMargins(16, 10, 16, 10);
        cardLayout->setSpacing(7);
        return qMakePair(card, cardLayout);
    };

    auto *generalSection = new QFrame;
    generalSection->setObjectName("settingsCard");
    auto *generalLayout = new QHBoxLayout(generalSection);
    generalLayout->setContentsMargins(16, 8, 16, 8);
    generalLayout->setSpacing(16);
    auto *generalTitle = makeLabel("本机协助", "font-size: 16px; font-weight: 650; color: #263446;");
    generalLayout->addWidget(generalTitle);
    auto *risk = makeLabel("PowerShell 将以当前 Windows 用户权限运行。这不是沙箱。", "font-size: 12px; color: #8a5b14; background: #fff6e5; border: 1px solid #f3dfb0; border-radius: 6px; padding: 7px 9px;");
    risk->setWordWrap(false);
    generalLayout->addWidget(risk, 1);
    settingsContentLayout->addWidget(generalSection);

    auto platformParts = makeSettingsCard();
    QFrame *platformSection = platformParts.first;
    QVBoxLayout *platformLayout = platformParts.second;
    addSectionHeader(platformLayout, "平台服务", "");
    platformAddress = new QLineEdit;
    platformAddress->setObjectName("settingsField");
    platformAddress->setMinimumHeight(32);
    platformAddress->setPlaceholderText("switchboard.example.com:25664");
    addFormRow(platformLayout, "服务器地址", platformAddress);

    platformPassword = new QLineEdit;
    platformPassword->setObjectName("settingsField");
    platformPassword->setMinimumHeight(32);
    platformPassword->setEchoMode(QLineEdit::Password);
    addFormRow(platformLayout, "Player 密码", platformPassword);

    platformTls = new QCheckBox("使用 TLS");
    platformTls->setChecked(true);
    addFormRow(platformLayout, "连接安全", platformTls);

    platformMode = new QComboBox;
    platformMode->setObjectName("settingsField");
    platformMode->setMinimumHeight(32);
    // Wheel events must not change the authorization mode while the user is
    // scrolling the settings page; only allow them when the box has focus.
    platformMode->setFocusPolicy(Qt::StrongFocus);
    platformMode->installEventFilter(this);
    platformMode->addItem("等待对方同意", "approval");
    platformMode->addItem("直接远程", "direct");
    addFormRow(platformLayout, "授权模式", platformMode);

    auto *platformActionRow = new QHBoxLayout;
    platformActionRow->setContentsMargins(110, 1, 0, 0);
    platformActionRow->setSpacing(12);
    platformConnectButton = makePrimaryButton(QStringLiteral("连接平台"), 40);
    platformConnectButton->setMinimumWidth(120);
    platformActionRow->addWidget(platformConnectButton);
    platformStatusValue = makeLabel("状态：平台未连接", "font-size: 12px; color: #69788d;");
    platformActionRow->addWidget(platformStatusValue);
    platformActionRow->addStretch();
    platformLayout->addLayout(platformActionRow);
    settingsContentLayout->addWidget(platformSection);

    auto *agentSection = new QFrame;
    agentSection->setObjectName("settingsCard");
    auto *agentLayout = new QHBoxLayout(agentSection);
    agentLayout->setContentsMargins(16, 8, 16, 8);
    auto *agentTitle = makeLabel("Agent 集成", "font-size: 16px; font-weight: 650; color: #263446;");
    agentLayout->addWidget(agentTitle);
    agentLayout->addStretch();
    auto *copyMcpConfig = makePrimaryButton(QStringLiteral("复制 Agent 配置"));
    agentLayout->addWidget(copyMcpConfig);
    settingsContentLayout->addWidget(agentSection);
    connect(copyMcpConfig, &QPushButton::clicked, this, [this] { copyMcpAgentConfig(); });

    auto *dataSection = new QFrame;
    dataSection->setObjectName("settingsCard");
    auto *dataLayout = new QHBoxLayout(dataSection);
    dataLayout->setContentsMargins(16, 8, 16, 8);
    auto *dataTitle = makeLabel("本地数据", "font-size: 16px; font-weight: 650; color: #263446;");
    dataLayout->addWidget(dataTitle);
    dataLayout->addStretch();
    auto *clearHistory = makeDangerButton(QStringLiteral("清除本地会话和任务记录"));
    dataLayout->addWidget(clearHistory);
    settingsContentLayout->addWidget(dataSection);
    settingsContentLayout->addStretch();

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
            showStatusMessage("授权模式已同步到平台", 1800);
        }
    });
    connect(clearHistory, &QPushButton::clicked, this, [this] {
        if (!askConfirm(this, "清除本地记录",
                        "清除所有本机会话和任务记录？\n\n正在运行的任务不会因此停止。清除后无法恢复。",
                        "清除记录"))
            return;
        sessions.clear();
        tasks.clear();
        persistSessionState();
        refreshSessionsPage();
        refreshActiveSessions();
    });
    return settingsPage;
}
