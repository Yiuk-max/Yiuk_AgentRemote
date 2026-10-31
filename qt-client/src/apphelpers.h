#pragma once

#include <QJsonArray>
#include <QLabel>
#include <QPushButton>
#include <QString>

// 窗口、页面构建器和活动窗口共用的小型控件工厂与格式化辅助函数
// 这些函数独立于 MainWindow，避免每个编译单元都依赖整个窗口类

QLabel *makeLabel(const QString &text, const QString &style = {});
QPushButton *makeButton(const QString &text, const QString &style = {});

// 页面和对话框共用的两种操作按钮样式
// height 只调整按钮尺寸；蓝色主按钮和红色危险操作样式集中定义
QPushButton *makePrimaryButton(const QString &text, int height = 0);
QPushButton *makeDangerButton(const QString &text, int height = 0);

// 配置根目录为 %LOCALAPPDATA%/Yiuk/DeviceAssist，同时仍支持读取旧版 %APPDATA% 路径
QString yiukConfigRoot();
QString legacyYiukConfigRoot();
QString yiukConfigPath(const QString &name);
QString findYiukConfigPath(const QString &name);

QString defaultCommandWorkingDirectory();

QString spacedCode(const QString &digits);
QString groupedPassword(const QString &password);
QString randomDigits(int count);
// 密码使用 32 字符的 base32 字符表，并排除语音沟通中容易混淆的字符
QString randomPassword(int count);
QString jsonArrayText(const QJsonArray &array);
