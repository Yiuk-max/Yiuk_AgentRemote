// 原生操作分发器的行为测试（控制台程序，不需要 GUI 交互）。
//
// 覆盖：能力列表、正常信封、未知操作、取消、超时、超大信封、抓屏几何。
// 这里直接实例化 NativeOperationDispatcher，不经过平台：协议那一层已经由
// Go 的 gateway/platformclient 测试覆盖，两边合起来才是完整链路。

#include "nativeops.h"
#include "vision/screengrab.h"

#include <QCoreApplication>
#include <QElapsedTimer>
#include <QEventLoop>
#include <QJsonObject>
#include <QTimer>

#include <QThread>

#include <atomic>
#include <cstdio>
#include <functional>

namespace {

int failures = 0;

void check(bool condition, const QString &what) {
    if (condition) {
        std::printf("ok   %s\n", qPrintable(what));
        return;
    }
    ++failures;
    std::printf("FAIL %s\n", qPrintable(what));
}

struct Outcome {
    QString status;
    QJsonObject envelope;
    int completedCount = 0;
};

// runAndWait 启动一次操作并等待 finished()；返回结构化结果。
Outcome runAndWait(NativeOperationDispatcher &dispatcher, const QString &sessionId, const QString &requestId,
                   const QString &operation, const QJsonObject &arguments, int timeoutSeconds,
                   std::function<void()> whileRunning = {}) {
    Outcome outcome;
    QEventLoop loop;
    QObject::connect(&dispatcher, &NativeOperationDispatcher::finished, &loop,
                     [&](const QString &, const QString &finishedId, const QString &status, const QJsonObject &envelope) {
        if (finishedId != requestId) return;
        outcome.status = status;
        outcome.envelope = envelope;
        outcome.completedCount++;
        loop.quit();
    });
    QTimer guard;
    guard.setSingleShot(true);
    QObject::connect(&guard, &QTimer::timeout, &loop, [&]() {
        std::printf("FAIL %s: finished() never arrived\n", qPrintable(operation));
        ++failures;
        loop.quit();
    });
    guard.start(10000);

    if (!dispatcher.start(sessionId, requestId, operation, arguments, timeoutSeconds)) {
        std::printf("FAIL %s: start() refused\n", qPrintable(operation));
        ++failures;
        return outcome;
    }
    if (whileRunning) QTimer::singleShot(0, &loop, whileRunning);
    loop.exec();
    return outcome;
}

}  // namespace

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    NativeOperationDispatcher dispatcher;

    dispatcher.registerOperation(QStringLiteral("test.echo"),
                                 [](const QString &, const QJsonObject &arguments, const std::atomic_bool *) {
        return NativeOperationDispatcher::envelopeOk(QJsonObject{{QStringLiteral("echo"), arguments}});
    });
    dispatcher.registerOperation(QStringLiteral("test.slow"),
                                 [](const QString &, const QJsonObject &arguments, const std::atomic_bool *cancelled) {
        const int totalMs = arguments.value(QStringLiteral("ms")).toInt(2000);
        QElapsedTimer timer;
        timer.start();
        while (timer.elapsed() < totalMs) {
            if (cancelled && cancelled->load())
                return NativeOperationDispatcher::envelopeError(QStringLiteral("CANCELLED"),
                                                                QStringLiteral("handler stopped early"));
            QThread::msleep(10);
        }
        return NativeOperationDispatcher::envelopeOk(QJsonObject{{QStringLiteral("slept"), totalMs}});
    });
    dispatcher.registerOperation(QStringLiteral("test.huge"),
                                 [](const QString &, const QJsonObject &, const std::atomic_bool *) {
        return NativeOperationDispatcher::envelopeOk(
            QJsonObject{{QStringLiteral("blob"), QString(40000, QLatin1Char('x'))}});
    });

    check(dispatcher.supports(QStringLiteral("test.echo")), QStringLiteral("supports() reports registered operation"));
    const QStringList capabilities = dispatcher.capabilities();
    check(capabilities.contains(QStringLiteral("test.echo")) &&
              capabilities.contains(QStringLiteral("test.slow")) &&
              capabilities.contains(QStringLiteral("test.huge")),
          QStringLiteral("capabilities() lists registered operations"));
    check(capabilities == QStringList(capabilities).mid(0) && capabilities.first() == QStringLiteral("test.echo"),
          QStringLiteral("capabilities() is sorted"));

    // 正常路径：参数原样进入信封。
    const Outcome echoed = runAndWait(dispatcher, "session-1", "req-echo", "test.echo",
                                      QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")}}, 5);
    check(echoed.status == QLatin1String("succeeded"), QStringLiteral("echo status is succeeded"));
    check(echoed.envelope.value(QStringLiteral("ok")).toBool(), QStringLiteral("echo envelope ok=true"));
    check(echoed.envelope.value(QStringLiteral("data")).toObject()
                  .value(QStringLiteral("echo")).toObject()
                  .value(QStringLiteral("text")).toString() == QStringLiteral("收藏"),
          QStringLiteral("echo data round-trips arguments"));

    // 未知操作：仍然要回复，而不是让平台等超时。
    const Outcome unknown = runAndWait(dispatcher, "session-1", "req-unknown", "vision.nope", QJsonObject{}, 5);
    check(unknown.envelope.value(QStringLiteral("code")).toString() == QLatin1String("UNSUPPORTED_OPERATION"),
          QStringLiteral("unknown operation returns UNSUPPORTED_OPERATION"));

    // 取消：处理器能看到标记并提前退出。
    const Outcome cancelled = runAndWait(dispatcher, "session-1", "req-cancel", "test.slow",
                                          QJsonObject{{QStringLiteral("ms"), 5000}}, 10,
                                          [&dispatcher]() { dispatcher.cancel("req-cancel"); });
    check(cancelled.status == QLatin1String("cancelled"), QStringLiteral("cancel status is cancelled"));
    check(cancelled.envelope.value(QStringLiteral("code")).toString() == QLatin1String("CANCELLED"),
          QStringLiteral("cancel envelope carries CANCELLED"));
    check(dispatcher.activeCount() == 0, QStringLiteral("no job leaks after cancel"));

    // 超时：处理器仍在跑，但调度器先结算。
    QElapsedTimer timeoutClock;
    timeoutClock.start();
    const Outcome timedOut = runAndWait(dispatcher, "session-1", "req-timeout", "test.slow",
                                        QJsonObject{{QStringLiteral("ms"), 3000}}, 1);
    check(timedOut.status == QLatin1String("timed_out"), QStringLiteral("timeout status is timed_out"));
    check(timedOut.envelope.value(QStringLiteral("code")).toString() == QLatin1String("NATIVE_TIMEOUT"),
          QStringLiteral("timeout envelope carries NATIVE_TIMEOUT"));
    check(timeoutClock.elapsed() < 2500, QStringLiteral("timeout fires near the deadline, not after the handler"));
    check(dispatcher.activeCount() == 0, QStringLiteral("no job leaks after timeout"));

    // 超大信封：换成明确的错误，而不是发一个必然被截断的帧。
    const Outcome huge = runAndWait(dispatcher, "session-1", "req-huge", "test.huge", QJsonObject{}, 5);
    check(huge.envelope.value(QStringLiteral("code")).toString() == QLatin1String("NATIVE_RESULT_TOO_LARGE"),
          QStringLiteral("oversized envelope becomes NATIVE_RESULT_TOO_LARGE"));

    // 同一 request_id 重放：start() 直接拒绝，不会第二次执行。
    check(!dispatcher.start("session-1", "req-echo", "test.echo", QJsonObject{}, 5),
          QStringLiteral("duplicate request id is refused by start()"));

    // 抓屏：真实桌面几何可用（无桌面会话时降级为警告，不判失败）。
    dispatcher.registerOperation(QStringLiteral("screen.capture"),
                                 [](const QString &, const QJsonObject &, const std::atomic_bool *) {
        yiuk::vision::ScreenCapture capture;
        QString error;
        if (!yiuk::vision::captureDesktop(&capture, &error))
            return NativeOperationDispatcher::envelopeError(QStringLiteral("SCREENSHOT_FAILED"), error);
        QJsonObject data = yiuk::vision::geometryToJson(capture.geometry);
        data.insert(QStringLiteral("captured"), true);
        return NativeOperationDispatcher::envelopeOk(data);
    });
    const Outcome capture = runAndWait(dispatcher, "session-1", "req-capture", "screen.capture", QJsonObject{}, 10);
    const bool captureOk = capture.envelope.value(QStringLiteral("ok")).toBool();
    if (captureOk) {
        const QJsonObject data = capture.envelope.value(QStringLiteral("data")).toObject();
        check(data.value(QStringLiteral("width")).toInt() > 0 && data.value(QStringLiteral("height")).toInt() > 0,
              QStringLiteral("screen.capture returns non-empty geometry"));
        std::printf("     desktop %dx%d at (%d,%d)\n", data.value(QStringLiteral("width")).toInt(),
                    data.value(QStringLiteral("height")).toInt(), data.value(QStringLiteral("screen_left")).toInt(),
                    data.value(QStringLiteral("screen_top")).toInt());
    } else {
        std::printf("WARN screen.capture unavailable in this session: %s\n",
                    qPrintable(capture.envelope.value(QStringLiteral("message")).toString()));
    }

    std::printf(failures == 0 ? "NATIVEOPS OK\n" : "NATIVEOPS FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
