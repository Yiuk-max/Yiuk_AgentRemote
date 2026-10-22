#include "mainwindow.h"


QWidget *MainWindow::createDevicesPage() {
    QVBoxLayout *layout = nullptr;
    devicesPage = pageShell("设备列表", "管理本机和已添加的设备。", &layout);
    auto *identity = new QFrame;
    identity->setObjectName("identityPanel");
    auto *card = new QVBoxLayout(identity);
    card->setContentsMargins(16, 10, 16, 10);
    card->setSpacing(4);
    auto *titleRow = new QHBoxLayout;
    titleRow->setContentsMargins(0, 0, 0, 0);
    titleRow->addWidget(makeLabel("本机设备", "font-size: 14px; font-weight: 650; color: #263446;"));
    titleRow->addStretch();
    card->addLayout(titleRow);
    auto *codeRow = new QHBoxLayout;
    codeRow->setContentsMargins(0, 0, 0, 0);
    deviceIdValue = makeLabel(deviceCode, "font-size: 20px; color: #172435;");
    codeRow->addWidget(deviceIdValue);
    codeRow->addStretch();
    auto *copy = iconButton(Glyph::Copy, "复制设备码");
    codeRow->addWidget(copy);
    card->addLayout(codeRow);
    connect(copy, &QPushButton::clicked, this, [this] { copyToClipboard(deviceCode, "设备码已复制"); });
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
    layout->addWidget(deviceScroll, 1);
    connect(refresh, &QPushButton::clicked, this, [this] { refreshDevicesPage(); showStatusMessage("设备列表已刷新", 1800); });
    refreshDevicesPage();
    return devicesPage;
}


QWidget *MainWindow::createSessionsPage() {
    QVBoxLayout *layout = nullptr;
    sessionsPage = pageShell("会话记录", "按时间记录每一笔连接及其双方设备码。", &layout);
    auto *scroll = new QScrollArea;
    scroll->setObjectName("sessionsScroll");
    scroll->setWidgetResizable(true);
    scroll->setFrameShape(QFrame::NoFrame);
    scroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    scroll->setVerticalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    auto *container = new QWidget;
    sessionsListLayout = new QVBoxLayout(container);
    sessionsListLayout->setContentsMargins(0, 0, 0, 0);
    sessionsListLayout->setSpacing(8);
    scroll->setWidget(container);
    layout->addWidget(scroll, 1);
    refreshSessionsPage();
    return sessionsPage;
}
