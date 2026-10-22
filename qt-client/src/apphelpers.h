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

// The two action flavours the pages and dialogs share. `height` only resizes
// the box; the blue primary and the red destructive looks stay defined once.
QPushButton *makePrimaryButton(const QString &text, int height = 0);
QPushButton *makeDangerButton(const QString &text, int height = 0);

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
