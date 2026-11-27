#include "mainwindow.h"
#include "yiukdialog.h"


QWidget *MainWindow::createDevicesPage() {
    QVBoxLayout *layout = nullptr;
    devicesPage = pageShell("设备列表", QString(), &layout);
    auto *listHeader = new QHBoxLayout;
    listHeader->addStretch();
    auto *refresh = iconButton(Glyph::Refresh, "刷新设备列表");
    listHeader->addWidget(refresh);
    auto *addDevice = makePrimaryButton(QStringLiteral("添加设备"), 36);
    listHeader->addWidget(addDevice);
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
    connect(addDevice, &QPushButton::clicked, this, [this] {
        QString entered;
        if (!askText(this, QStringLiteral("添加收藏设备"),
                     QStringLiteral("对方的 9 位设备码"),
                     QLineEdit::Normal, QString(), &entered, QStringLiteral("添加")))
            return;
        QString digits;
        for (const QChar ch : entered) if (ch.isDigit()) digits += ch;
        if (digits.size() != 9) {
            showWarningMessage(this, "设备码无效", "请输入完整的 9 位设备码。");
            return;
        }
        const QString formatted = formatDeviceCode(digits);
        if (formatted == formatDeviceCode(deviceCode)) {
            showWarningMessage(this, "设备码无效", "不能将本机添加到收藏设备。");
            return;
        }
        savedDevices = QSettings().value("savedDevices").toStringList();
        if (savedDevices.contains(formatted)) {
            showStatusMessage("设备已在收藏列表", 1800);
            return;
        }
        savedDevices.append(formatted);
        QSettings().setValue("savedDevices", savedDevices);
        refreshDevicesPage();
        showStatusMessage("设备已添加", 1800);
    });
    refreshDevicesPage();
    return devicesPage;
}


QWidget *MainWindow::createSessionsPage() {
    QVBoxLayout *layout = nullptr;
    sessionsPage = pageShell("会话记录", QString(), &layout);
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
