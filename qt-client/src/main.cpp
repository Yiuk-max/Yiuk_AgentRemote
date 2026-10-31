#include "mainwindow.h"
#include "apphelpers.h"

#include <QApplication>
#include <QDir>
#include <QLocalServer>
#include <QLocalSocket>
#include <QLockFile>
#include <QPalette>


int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    app.setApplicationName("Yiuk Device Assist");
    app.setOrganizationName("Yiuk");

    // The local bridge and the platform registration both identify this machine by
    // device code, so allowing two GUI processes would create duplicate owners.
    const QString configRoot = yiukConfigRoot();
    QDir().mkpath(configRoot);
    QLockFile instanceLock(QDir(configRoot).filePath("client-instance.lock"));
    instanceLock.setStaleLockTime(5000);
    const QString instanceServerName = QStringLiteral("Yiuk.DeviceAssist.SingleInstance");
    if (!instanceLock.tryLock(100)) {
        QLocalSocket socket;
        socket.connectToServer(instanceServerName, QIODevice::WriteOnly);
        if (socket.waitForConnected(1500)) {
            socket.write("show\n");
            socket.waitForBytesWritten(500);
        }
        return 0;
    }
    QLocalServer::removeServer(instanceServerName);
    QLocalServer instanceServer;
    instanceServer.listen(instanceServerName);

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
    // 工具提示是没有父控件的顶层弹窗，无法继承 MainWindow 的白色底板样式
    // 其他样式仍由 MainWindow 提供
    app.setStyleSheet("QToolTip { background: #ffffff; color: #263446; border: 1px solid #e1e7ef; "
                      "border-radius: 6px; padding: 5px 8px; font-size: 12px; }");
    MainWindow window;
    QObject::connect(&instanceServer, &QLocalServer::newConnection, &window, [&] {
        while (instanceServer.hasPendingConnections()) {
            QLocalSocket *socket = instanceServer.nextPendingConnection();
            QObject::connect(socket, &QLocalSocket::readyRead, &window, [socket, &window] {
                socket->readAll();
                window.showNormal();
                window.raise();
                window.activateWindow();
            });
            QObject::connect(socket, &QLocalSocket::disconnected, socket, &QObject::deleteLater);
        }
    });
    window.show();
    return app.exec();
}
