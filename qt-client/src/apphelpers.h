#pragma once

#include <QJsonArray>
#include <QLabel>
#include <QPushButton>
#include <QString>

// Small widget factories and formatting helpers shared by the window, the page
// builders and the activity window. They live outside MainWindow so each
// translation unit can use them without pulling in the whole window class.

QLabel *makeLabel(const QString &text, const QString &style = {});
QPushButton *makeButton(const QString &text, const QString &style = {});

// Configuration root: %LOCALAPPDATA%/Yiuk/DeviceAssist, with the legacy
// %APPDATA% location still honoured for reading.
QString yiukConfigRoot();
QString legacyYiukConfigRoot();
QString yiukConfigPath(const QString &name);
QString findYiukConfigPath(const QString &name);

QString defaultCommandWorkingDirectory();

QString spacedCode(const QString &digits);
QString groupedPassword(const QString &password);
QString randomDigits(int count);
// 32-character base32 alphabet password; the alphabet omits characters that
// are easy to confuse over a voice channel.
QString randomPassword(int count);
QString jsonArrayText(const QJsonArray &array);
