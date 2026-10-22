#include "mainwindow.h"

#include <QApplication>
#include <QPalette>


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
