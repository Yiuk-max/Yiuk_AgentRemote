// 本机 MCP 桥的行为测试（loopback + token，两个通道）。
//
// 覆盖：token 校验、普通工具的 32 KiB 上限、原生通道的异步回包与更大的请求体上限。
// 原生通道必须异步：抓屏 30ms、OCR 300ms、等目标出现可以到 20s，同步会把 GUI 线程按住。
#include "localbridge.h"

#include <QCoreApplication>
#include <QElapsedTimer>
#include <QEventLoop>
#include <QHostAddress>
#include <QJsonDocument>
#include <QJsonObject>
#include <QTcpSocket>
#include <QTimer>

#include <cstdio>

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

struct Reply {
    int status = 0;
    QJsonObject body;
    qint64 elapsedMs = 0;
};

// 直接往 bridge 发一个 POST，读到连接关闭为止。
Reply post(LocalBridge *bridge, const QString &path, const QByteArray &body, bool withToken, int timeoutMs = 8000) {
    Reply reply;
    QElapsedTimer timer;
    timer.start();
    QTcpSocket socket;
    socket.connectToHost(QHostAddress::LocalHost, static_cast<quint16>(bridge->serverPort()));
    if (!socket.waitForConnected(2000)) return reply;

    QByteArray request = "POST " + path.toUtf8() + " HTTP/1.1\r\nHost: 127.0.0.1\r\n";
    request += "Content-Type: application/json\r\n";
    request += "Content-Length: " + QByteArray::number(body.size()) + "\r\n";
    if (withToken) request += "Authorization: Bearer " + bridge->token().toLatin1() + "\r\n";
    request += "\r\n" + body;
    socket.write(request);
    socket.flush();

    QByteArray received;
    QEventLoop loop;
    QTimer guard;
    guard.setSingleShot(true);
    QObject::connect(&guard, &QTimer::timeout, &loop, &QEventLoop::quit);
    QObject::connect(&socket, &QTcpSocket::readyRead, &loop, [&]() { received += socket.readAll(); });
    QObject::connect(&socket, &QTcpSocket::disconnected, &loop, &QEventLoop::quit);
    guard.start(timeoutMs);
    loop.exec();

    reply.elapsedMs = timer.elapsed();
    const qsizetype headerEnd = received.indexOf("\r\n\r\n");
    if (headerEnd < 0) return reply;
    const QList<QByteArray> statusLine = received.left(headerEnd).split('\n').first().trimmed().split(' ');
    if (statusLine.size() >= 2) reply.status = statusLine.at(1).toInt();
    reply.body = QJsonDocument::fromJson(received.mid(headerEnd + 4)).object();
    return reply;
}

QByteArray paddedBody(int bytes) {
    QJsonObject object{{"text", QString(bytes, QLatin1Char('x'))}};
    return QJsonDocument(object).toJson(QJsonDocument::Compact);
}

}  // namespace

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);

    bool nativeHandlerCalled = false;
    LocalBridge bridge(
        [](const QString &tool, const QJsonObject &) {
            return QJsonObject{{"ok", true}, {"code", "OK"}, {"data", QJsonObject{{"tool", tool}}}};
        },
        nullptr);
    // 异步：50ms 之后才回包。如果 bridge 是同步模型，测试会在收到响应前先看到空结果。
    bridge.setNativeHandler([&nativeHandlerCalled](const QJsonObject &request, LocalBridge::NativeResponder respond) {
        nativeHandlerCalled = true;
        QTimer::singleShot(50, [request, respond = std::move(respond)]() {
            respond(QJsonObject{{"ok", true},
                                {"code", "OK"},
                                {"data", QJsonObject{{"operation", request.value("operation").toString()},
                                                     {"delayed", true}}}});
        });
    });
    QString error;
    check(bridge.start(&error), QStringLiteral("local bridge listens on loopback: %1").arg(error));
    check(bridge.token().size() >= 32, QStringLiteral("bridge token is long enough"));
    check(bridge.endpoint().startsWith(QStringLiteral("http://127.0.0.1:")),
          QStringLiteral("bridge endpoint is loopback http"));

    // 1) 普通工具：同步返回。
    const Reply tool = post(&bridge, QStringLiteral("/tool/list_devices"), "{}", true);
    check(tool.status == 200 && tool.body.value(QStringLiteral("ok")).toBool(),
          QStringLiteral("tool channel answers synchronously"));
    check(tool.body.value(QStringLiteral("data")).toObject().value(QStringLiteral("tool")).toString() ==
              QStringLiteral("list_devices"),
          QStringLiteral("tool name reaches the handler"));

    // 2) 原生通道：异步回包，且真的等到了处理器。
    const Reply native = post(&bridge, QStringLiteral("/native"),
                              QJsonDocument(QJsonObject{{"session_id", "s1"}, {"operation", "vision.find"},
                                                        {"arguments", QJsonObject{{"text", "收藏"}}}})
                                  .toJson(QJsonDocument::Compact),
                              true);
    check(nativeHandlerCalled, QStringLiteral("native handler is invoked"));
    check(native.status == 200 && native.body.value(QStringLiteral("ok")).toBool(),
          QStringLiteral("native channel replies ok"));
    check(native.body.value(QStringLiteral("data")).toObject().value(QStringLiteral("delayed")).toBool(),
          QStringLiteral("native reply comes from the async completion"));
    check(native.body.value(QStringLiteral("data")).toObject().value(QStringLiteral("operation")).toString() ==
              QStringLiteral("vision.find"),
          QStringLiteral("native operation is forwarded to the handler"));
    check(native.elapsedMs >= 40, QStringLiteral("native reply waits for the handler (async, not sync)"));

    // 3) token 与端点校验。
    const Reply unauthorized = post(&bridge, QStringLiteral("/tool/list_devices"), "{}", false);
    check(unauthorized.status == 401, QStringLiteral("missing token is 401"));
    const Reply unknown = post(&bridge, QStringLiteral("/etc/passwd"), "{}", true);
    check(unknown.status == 404, QStringLiteral("unknown endpoint is 404"));

    // 4) 请求体上限：普通工具 32 KiB，原生操作 128 KiB + 余量。
    const Reply toolTooLarge = post(&bridge, QStringLiteral("/tool/execute_command"), paddedBody(40 << 10), true);
    check(toolTooLarge.status == 413, QStringLiteral("tool channel rejects bodies over 32 KiB"));
    const Reply nativeBig = post(&bridge, QStringLiteral("/native"),
                                 QJsonDocument(QJsonObject{{"session_id", "s1"},
                                                           {"operation", "vision.find"},
                                                           {"arguments", QJsonObject{{"image_b64", QString(100 << 10,
                                                                                                         QLatin1Char('A'))}}}})
                                     .toJson(QJsonDocument::Compact),
                                 true);
    check(nativeBig.status == 200, QStringLiteral("native channel accepts bodies up to 128 KiB"));
    const Reply nativeTooLarge = post(&bridge, QStringLiteral("/native"),
                                      QJsonDocument(QJsonObject{{"session_id", "s1"},
                                                                {"operation", "vision.find"},
                                                                {"arguments", QJsonObject{{"image_b64", QString(200 << 10,
                                                                                                              QLatin1Char('A'))}}}})
                                          .toJson(QJsonDocument::Compact),
                                      true);
    check(nativeTooLarge.status == 413, QStringLiteral("native channel still bounds the body"));

    std::printf(failures == 0 ? "LOCALBRIDGE OK\n" : "LOCALBRIDGE FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
