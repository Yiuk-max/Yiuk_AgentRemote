#include "mainwindow.h"


QWidget *MainWindow::createDevicesPage() {
    QVBoxLayout *layout = nullptr;
    devicesPage = pageShell("设备列表", "管理本机和已添加的设备。", &layout);
    auto *identity = new QFrame;
    identity->setObjectName("identityPanel");
    auto *card = new QVBoxLayout(identity);
    card->setContentsMargins(16, 12, 16, 12);
    card->setSpacing(8);
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
        const QString path = yiukConfigPath("device.json");
        QFile::remove(path);
        deviceCode = loadOrCreateDeviceCode();
        refreshDevicesPage();
    });
    layout->addWidget(identity);
    layout->addSpacing(18);

    auto *listHeader = new QHBoxLayout;
    listHeader->addStretch();
    auto *refresh = iconButton(Glyph::Refresh, "刷新设备列表");
    listHeader->addWidget(refresh);
    layout->addLayout(listHeader);
    auto *deviceListContainer = new QWidget;
    deviceListLayout = new QVBoxLayout(deviceListContainer);
    deviceListLayout->setContentsMargins(0, 0, 0, 0);
    deviceListLayout->setSpacing(8);
    auto *deviceScroll = new QScrollArea;
    deviceScroll->setWidget(deviceListContainer);
    deviceScroll->setWidgetResizable(true);
    deviceScroll->setFrameShape(QFrame::NoFrame);
    deviceScroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    deviceScroll->setSizeAdjustPolicy(QAbstractScrollArea::AdjustToContents);
    deviceScroll->setMinimumHeight(0);
    deviceScroll->setMaximumHeight(260);
    layout->addWidget(deviceScroll, 0);
    layout->addStretch(1);
    connect(refresh, &QPushButton::clicked, this, [this] { refreshDevicesPage(); statusBar()->showMessage("设备列表已刷新", 1800); });
    refreshDevicesPage();
    return devicesPage;
}


QWidget *MainWindow::createSessionsPage() {
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
