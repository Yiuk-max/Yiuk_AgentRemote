// Opt-in end-to-end harness: real MainWindow + real question UI and transport.
// Isolate LOCALAPPDATA before launching; the Go test uses a temporary directory.
#include "mainwindow.h"
#include "yiukdialog.h"

#include <QApplication>
#include <QJsonDocument>
#include <QKeyEvent>
#include <QLineEdit>
#include <QPushButton>
#include <QScreen>
#include <cstdio>
#ifdef Q_OS_WIN
#include <windows.h>
#endif

void output(const QJsonObject &message) {
    const auto bytes = QJsonDocument(message).toJson(QJsonDocument::Compact);
    fwrite(bytes.constData(), 1, bytes.size(), stdout);
    fputc('\n', stdout);
    fflush(stdout);
}

namespace {
YiukDialog *activeQuestion() {
    for (auto *widget : QApplication::topLevelWidgets())
        if (widget->objectName() == QLatin1String("userQuestionDialog") && widget->isVisible())
            return static_cast<YiukDialog *>(widget);
    return nullptr;
}
QPushButton *buttonWithText(QWidget *root, const QString &text) {
    for (auto *button : root->findChildren<QPushButton *>())
        if (button->text() == text) return button;
    return nullptr;
}
}  // namespace

#ifdef Q_OS_WIN
namespace {
// 后台进程无法直接抢前台：附加到当前前台线程再 SetForegroundWindow 才可靠。
// 这是测试程序专用，产品代码不应这样做。
void forceForeground(HWND hwnd) {
    if (!hwnd) return;
    HWND foreground = GetForegroundWindow();
    const DWORD foregroundThread = foreground ? GetWindowThreadProcessId(foreground, nullptr) : 0;
    const DWORD thisThread = GetCurrentThreadId();
    if (foregroundThread && foregroundThread != thisThread) AttachThreadInput(foregroundThread, thisThread, TRUE);
    ShowWindow(hwnd, SW_SHOW);
    BringWindowToTop(hwnd);
    SetForegroundWindow(hwnd);
    SetFocus(hwnd);
    if (foregroundThread && foregroundThread != thisThread) AttachThreadInput(foregroundThread, thisThread, FALSE);
}
}  // namespace
#endif

int main(int argc, char **argv) {
    QApplication app(argc, argv);
    app.setQuitOnLastWindowClosed(false);
    app.setApplicationName("YiukQuestionTest");
    app.setOrganizationName("YiukTests");
    app.setStyle("Fusion");
    app.setFont(QFont(QStringLiteral("Microsoft YaHei UI"), 9));
    QPalette palette;
    palette.setColor(QPalette::Window, QColor("#ffffff"));
    palette.setColor(QPalette::Base, QColor("#ffffff"));
    palette.setColor(QPalette::WindowText, QColor("#182230"));
    palette.setColor(QPalette::Text, QColor("#182230"));
    palette.setColor(QPalette::Highlight, QColor("#1479ed"));
    app.setPalette(palette);
    // Same DPAPI storage and identity loading as the shipped executable, but
    // under the test's temporary LOCALAPPDATA. No production credentials touched.
    const QString playerPassword = qEnvironmentVariable("YIUK_PLATFORM_PASSWORD");
    saveYiukIdentity({{"device_id", "123456789"}, {"address", qEnvironmentVariable("YIUK_PLATFORM_ADDRESS")},
        {"player_password", SecureStore::protect(playerPassword)},
        {"temporary_password", SecureStore::protect("0123456789abcdef0123456789abcdef")},
        {"mode", "direct"}, {"tls", false}});
    MainWindow window;
    // Deliberately keep the main window hidden, to exercise tray-resident use.
    auto *device = window.findChild<PlatformClient *>();
    auto *questions = window.findChild<UserQuestion *>();
    if (!device || !questions) return 1;

    if (app.arguments().contains(QStringLiteral("--pages"))) {
        window.resize(960, 600);
        window.show();
        const QString base = qEnvironmentVariable("YIUK_QUESTION_SNAPSHOT");
        QTimer::singleShot(300, &app, [&] {
            const auto navs = window.findChildren<QPushButton *>(QStringLiteral("navItem"));
            for (int i = 0; i < navs.size(); ++i) {
                navs.at(i)->click();
                QCoreApplication::processEvents();
                QCoreApplication::processEvents();
                if (!base.isEmpty()) window.grab().save(QStringLiteral("%1.page%2.png").arg(base).arg(i));
            }
            app.exit(0);
        });
        return app.exec();
    }

    if (app.arguments().contains(QStringLiteral("--desktop"))) {
        // Foreground test surface for press_keys / type_text / move_mouse.
        // The Go test skips unless this window reports itself active, so a
        // stray keystroke can never land in somebody else's application.
        auto *surface = new QWidget;
        surface->setWindowTitle(QStringLiteral("Yiuk Desktop Test"));
        surface->resize(520, 160);
        surface->setStyleSheet("background: #ffffff;");
        auto *surfaceLayout = new QVBoxLayout(surface);
        surfaceLayout->setContentsMargins(16, 16, 16, 16);
        auto *edit = new QLineEdit;
        edit->setObjectName("desktopInput");
        edit->setMinimumHeight(36);
        edit->setStyleSheet("QLineEdit { border: 1px solid #cbd5e1; border-radius: 6px; padding: 6px 8px; font-size: 14px; }");
        surfaceLayout->addWidget(edit);
        // 一个点得动的按钮 + 会变的标签：用来验证 click 是否真的落地，
        // 以及 detect_change 能不能看出“画面变了”。
        auto *label = new QLabel(QStringLiteral("点击次数：0"));
        label->setObjectName("desktopLabel");
        surfaceLayout->addWidget(label);
        auto *button = new QPushButton(QStringLiteral("点我"));
        button->setObjectName("desktopButton");
        button->setMinimumHeight(36);
        surfaceLayout->addWidget(button);
        int clicks = 0;
        QObject::connect(button, &QPushButton::clicked, &app, [&] {
            ++clicks;
            label->setText(QStringLiteral("点击次数：%1").arg(clicks));
            output({{"clicked", clicks}});
        });
        surface->show();
        surface->raise();
        surface->activateWindow();
        edit->setFocus();
#ifdef Q_OS_WIN
        forceForeground(reinterpret_cast<HWND>(surface->winId()));
#endif
        const QRect rect = surface->geometry();
        const QRect buttonRect(button->mapToGlobal(QPoint(0, 0)), button->size());
        const QRect editRect(edit->mapToGlobal(QPoint(0, 0)), edit->size());
        const QPoint editProbe = editRect.topLeft() + QPoint(editRect.width() / 2, editRect.height() / 2);
        output({{"ready", true}, {"rect_x", rect.x()}, {"rect_y", rect.y()},
                {"rect_w", rect.width()}, {"rect_h", rect.height()},
                {"button_x", buttonRect.x()}, {"button_y", buttonRect.y()},
                {"button_w", buttonRect.width()}, {"button_h", buttonRect.height()},
                {"edit_x", editRect.x()}, {"edit_y", editRect.y()},
                {"edit_cx", editProbe.x()}, {"edit_cy", editProbe.y()}});
        QObject::connect(edit, &QLineEdit::textChanged, &app, [&](const QString &text) { output({{"text", text}}); });

        QTimer housekeeping;
        housekeeping.setInterval(120);
        QObject::connect(&housekeeping, &QTimer::timeout, &app, [&] {
            // Auto-approve the local bridge session the test requests.
            for (auto *button : window.findChildren<QPushButton *>())
                if (button->text() == QStringLiteral("批准")) { button->click(); break; }
#ifdef Q_OS_WIN
            static QString lastForeground;
            wchar_t foregroundTitle[300] = {};
            HWND foreground = GetForegroundWindow();
            GetWindowTextW(foreground, foregroundTitle, 300);
            const QString currentForeground = QString::fromWCharArray(foregroundTitle);
            if (currentForeground != lastForeground) {
                lastForeground = currentForeground;
                output({{"foreground", currentForeground}, {"foregroundIsSurface", foreground == reinterpret_cast<HWND>(surface->winId())}});
            }
#endif
#ifdef Q_OS_WIN
            if (GetForegroundWindow() != reinterpret_cast<HWND>(surface->winId()))
                forceForeground(reinterpret_cast<HWND>(surface->winId()));
#endif
            static bool reported = false;
            const bool active = surface->isActiveWindow();
            if (!reported || active != reported) {
                reported = active;
                output({{"active", active}});
            }
            const auto commandFile = qEnvironmentVariable("YIUK_QUESTION_CONTROL");
            if (commandFile.isEmpty() || !QFile::exists(commandFile)) return;
            QFile command(commandFile);
            if (!command.open(QIODevice::ReadOnly)) return;
            const auto action = command.readAll().trimmed();
            command.close();
            QFile::remove(commandFile);
            if (action == "quit") {
                app.quit();
            } else if (action == "cursor") {
#ifdef Q_OS_WIN
                POINT cursor{};
                GetCursorPos(&cursor);
                QJsonArray position;
                position.append(static_cast<int>(cursor.x));
                position.append(static_cast<int>(cursor.y));
                output(QJsonObject{{"cursor", position}});
#endif
            }
        });
        housekeeping.start();
        QTimer::singleShot(60000, &app, [&] { app.exit(9); });
        return app.exec();
    }

    if (app.arguments().contains(QStringLiteral("--preview"))) {
        const QString variant = qEnvironmentVariable("YIUK_PREVIEW", "options");
        QJsonObject request{{"session_id", "preview"}, {"request_id", "preview"},
                            {"expires_at", QDateTime::currentSecsSinceEpoch() + 300}};
        if (variant == "input") {
            request["question"] = QStringLiteral("这台电脑上有没有需要我优先处理的东西？");
        } else if (variant == "extreme") {
            request["question"] = QStringLiteral("请确认你的选择，").repeated(100);
            QJsonArray many;
            for (int i = 0; i < 6; ++i)
                many.append(QStringLiteral("较长选项文字").repeated(15) + QString::number(i));
            request["options"] = many;
        } else if (variant == "long") {
            request["question"] = QStringLiteral("我准备开始远程操作，但有些步骤会改动系统设置。请确认你希望我按哪种方式继续，我会严格按你的选择执行，不会擅自扩大到其他范围。");
            request["options"] = QJsonArray{
                QStringLiteral("只做你刚才说的那一件事，其他都不要动"),
                QStringLiteral("可以顺带清理临时文件，但不要改注册表"),
                QStringLiteral("先把计划说清楚，我确认后再开始"),
                QStringLiteral("暂时不要操作，等我自己看完再说"),
                QStringLiteral("先备份，再继续"),
                QStringLiteral("其他（我在下面自己写）")};
        } else {
            request["question"] = QStringLiteral("接下来你希望我先做什么？");
            request["options"] = QJsonArray{QStringLiteral("检查并修复软件问题"),
                                            QStringLiteral("安装或配置软件"),
                                            QStringLiteral("整理文件，先给出方案")};
        }
        const QString error = questions->ask(request);
        if (!error.isEmpty()) { output({{"error", error}}); app.exit(5); return app.exec(); }
        QTimer::singleShot(400, &app, [&] {
            YiukDialog *popup = activeQuestion();
            if (!popup) { output({{"error", "window missing"}}); app.exit(2); return; }
            const QString snapshot = qEnvironmentVariable("YIUK_QUESTION_SNAPSHOT");
            const QString result = snapshot.isEmpty() ? QString() : snapshot + "." + variant + ".png";
            if (!result.isEmpty()) popup->grab().save(result);
            app.exit(0);
        });
        return app.exec();
    }

    QObject::connect(device, &PlatformClient::temporaryPasswordUpdated, &app, [&](const QString &password, int, qint64) {
        output({{"ready", true}, {"device_id", "123456789"}, {"password", password}});
    });
    QObject::connect(device, &PlatformClient::credentialsRenewed, &app, [&](const QString &password, int) {
        output({{"ready", true}, {"device_id", "123456789"}, {"password", password}});
    });
    QObject::connect(questions, &UserQuestion::finished, &app, [&](const QJsonObject &reply) {
        output({{"finished", reply.value("status")}});
    });
    QObject::connect(device, &PlatformClient::userQuestionRequested, &app, [&](const QJsonObject &request) {
        output({{"shown", true}, {"request_id", request.value("request_id")}});
        // Repeated delivery must reuse the same pending window, never stack.
        if (!questions->ask(request).isEmpty()) { output({{"error", "duplicate request rejected"}}); app.exit(12); return; }
        auto other = request;
        other["request_id"] = "concurrent-question";
        if (questions->ask(other) != "QUESTION_BUSY") { output({{"error", "concurrent question accepted"}}); app.exit(13); return; }
        auto invalid = request;
        invalid["options"] = QJsonArray{QStringLiteral("同一个"), QStringLiteral(" 同一个 ")};
        if (UserQuestion::validRequest(invalid)) { output({{"error", "duplicate option accepted"}}); app.exit(14); return; }
        const auto question = request.value("question").toString();
        const auto mode = question.startsWith("long:") ? QStringLiteral("long") : question;
        const auto requestId = request.value("request_id").toString();
        const auto sessionId = request.value("session_id").toString();
        QTimer::singleShot(80, questions, [&, mode, requestId, sessionId] {
            // A stop-session request may dismiss the UI before automation runs.
            if (questions->result(sessionId, requestId).value("status").toString() != "pending") return;
            YiukDialog *popup = activeQuestion();
            if (!popup) { output({{"error", "window missing"}}); app.exit(3); return; }
            auto *edit = popup->findChild<QLineEdit *>("settingsField");
            auto *send = popup->findChild<QPushButton *>("primary");
            auto *decline = buttonWithText(popup, QStringLiteral("暂不回答"));
            if (!edit || !send || !decline || send->isEnabled() || popup->isModal() ||
                !(popup->windowFlags() & Qt::FramelessWindowHint)) {
                output({{"error", "initial UI invariant"}});
                app.exit(4);
                return;
            }
            if (!popup->screen()->availableGeometry().contains(popup->frameGeometry())) {
                output({{"error", "popup outside available screen"}});
                app.exit(7);
                return;
            }
            if (mode == "wait" || mode == "timeout") return;
            if (mode == "escape") {
                QKeyEvent escape(QEvent::KeyPress, Qt::Key_Escape, Qt::NoModifier);
                QApplication::sendEvent(popup, &escape);
                return;
            }
            // Enter in an empty input must not answer with the first option.
            QKeyEvent enter(QEvent::KeyPress, Qt::Key_Return, Qt::NoModifier);
            QApplication::sendEvent(edit, &enter);
            if (questions->result(sessionId, requestId).value("status").toString() != "pending") { output({{"error", "Enter submitted an empty answer"}}); app.exit(8); return; }
            if (mode == "cancel" || mode == "close") {
                if (mode == "cancel") decline->click();
                else popup->close();
                return;
            }
            if (mode == "text" || mode == "input" || mode == "long") {
                edit->setText(QStringLiteral("字").repeated(1001));
                if (send->isEnabled()) { output({{"error", "oversized text accepted"}}); app.exit(10); return; }
                edit->setText(QStringLiteral("自定义答案 😀"));
                if (!send->isEnabled()) { output({{"error", "valid text did not enable send"}}); app.exit(11); return; }
                if (mode == "text") QApplication::sendEvent(edit, &enter);
                else send->click();
                return;
            }
            auto *choice = popup->findChild<QPushButton *>("userQuestionOption1");
            if (!choice) { app.exit(5); return; }
            choice->click();
        });
    });
    QObject::connect(device, &PlatformClient::platformError, &app, [&](const QString &code, const QString &message) {
        output({{"platform_error", code}, {"message", message}});
    });
    QObject::connect(device, &PlatformClient::stateChanged, &app, [&](const QString &state) {
        if (state == "disconnected") output({{"disconnected", true}});
    });
    QTimer control;
    control.setInterval(100);
    QObject::connect(&control, &QTimer::timeout, &app, [&] {
        const auto commandFile = qEnvironmentVariable("YIUK_QUESTION_CONTROL");
        if (commandFile.isEmpty() || !QFile::exists(commandFile)) return;
        QFile command(commandFile);
        if (!command.open(QIODevice::ReadOnly)) return;
        const auto action = command.readAll().trimmed();
        command.close();
        QFile::remove(commandFile);
        if (action == "disconnect") device->disconnectFromPlatform();
        else if (action == "quit") app.quit();
    });
    control.start();
    if (qEnvironmentVariable("YIUK_QUESTION_LOCAL") == "1") {
        // Exercise the same window via the actual HTTP local bridge. Approve a
        // local request by clicking the real pending-request button.
        QObject::connect(&control, &QTimer::timeout, &app, [&] {
            for (auto *button : window.findChildren<QPushButton *>())
                if (button->text() == QStringLiteral("批准")) { button->click(); break; }
            YiukDialog *popup = activeQuestion();
            if (!popup) return;
            auto *edit = popup->findChild<QLineEdit *>("settingsField");
            auto *send = popup->findChild<QPushButton *>("primary");
            if (edit && send) { edit->setText(QStringLiteral("本地桥接回复")); send->click(); }
        });
    }
    QTimer::singleShot(90000, &app, [&] { app.exit(9); });
    return app.exec();
}
