#include "localbridge.h"

#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QHostAddress>
#include <QJsonDocument>
#include <QJsonParseError>
#include <QPointer>
#include <QRandomGenerator>
#include <QTcpSocket>
#include <QTimer>

namespace {

// 收到请求后到回包之前的兜底：普通工具很快，原生操作最长 25 秒（平台单次上限）。
constexpr int kReplyDeadlineMs = 30000;

QByteArray reasonPhrase(int statusCode) {
    switch (statusCode) {
        case 200:
            return "OK";
        case 400:
            return "Bad Request";
        case 401:
            return "Unauthorized";
        case 404:
            return "Not Found";
        case 405:
            return "Method Not Allowed";
        case 413:
            return "Payload Too Large";
        default:
            return "Error";
    }
}

}  // namespace

LocalBridge::LocalBridge(ToolHandler toolHandler, QObject *parent)
    : QTcpServer(parent), toolHandler_(std::move(toolHandler)) {
    const QByteArray tokenSeed = QByteArray::number(QRandomGenerator::global()->generate64()) +
        QByteArray::number(QRandomGenerator::global()->generate64());
    token_ = QString::fromLatin1(QCryptographicHash::hash(tokenSeed, QCryptographicHash::Sha256).toHex());
}

void LocalBridge::setNativeHandler(NativeHandler handler) { nativeHandler_ = std::move(handler); }

QString LocalBridge::token() const { return token_; }
QString LocalBridge::endpoint() const { return QStringLiteral("http://127.0.0.1:%1").arg(serverPort()); }

bool LocalBridge::start(QString *error) {
    if (!listen(QHostAddress(QStringLiteral("127.0.0.1")), 0)) {
        if (error) *error = errorString();
        return false;
    }
    return true;
}

void LocalBridge::incomingConnection(qintptr socketDescriptor) {
    auto *socket = new QTcpSocket(this);
    if (!socket->setSocketDescriptor(socketDescriptor)) {
        socket->deleteLater();
        return;
    }
    socket->setProperty("requestBuffer", QByteArray{});
    connect(socket, &QTcpSocket::readyRead, this, [this, socket] { readAndDispatchRequest(socket); });
    connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
    QTimer::singleShot(kReplyDeadlineMs, socket, [socket] {
        if (socket->state() == QAbstractSocket::ConnectedState && !socket->property("responded").toBool())
            socket->disconnectFromHost();
    });
}

void LocalBridge::readAndDispatchRequest(QTcpSocket *socket) {
    // 超限的请求体要先把数据读掉再回 413：带着未读数据关连接，客户端只会看到
    // “连接被重置”，拿不到明确的上限错误。这里不缓存、只丢弃。
    if (socket->property("discardLeft").isValid()) {
        qint64 left = socket->property("discardLeft").toLongLong();
        const qint64 take = qMin(socket->bytesAvailable(), left);
        socket->read(take);
        left -= take;
        socket->setProperty("discardLeft", left);
        if (left > 0) return;
        sendHttpResponse(socket, 413, socket->property("rejectBody").toJsonObject());
        return;
    }

    QByteArray buffer = socket->property("requestBuffer").toByteArray();
    buffer += socket->readAll();
    // 先按最大可能的上限挡住明显的洪水；具体是 /native 还是 /tool 等解析出路径再判。
    if (buffer.size() > kNativeBodyLimit + (8 << 10)) {
        // 连 header 都不完整却已经把缓冲写满：直接断开，不陪它耗内存。
        sendHttpResponse(socket, 413, {{"ok", false}, {"code", "REQUEST_TOO_LARGE"}, {"message", "request is too large"}});
        return;
    }
    socket->setProperty("requestBuffer", buffer);
    const qsizetype headerEnd = buffer.indexOf("\r\n\r\n");
    if (headerEnd < 0) return;

    const QList<QByteArray> headerLines = buffer.left(headerEnd).split('\n');
    if (headerLines.isEmpty()) {
        sendHttpResponse(socket, 400, {{"ok", false}, {"code", "BAD_REQUEST"}, {"message", "invalid HTTP request"}});
        return;
    }
    const QList<QByteArray> requestLine = headerLines.first().trimmed().split(' ');
    if (requestLine.size() != 3 || requestLine[0] != "POST") {
        sendHttpResponse(socket, 405, {{"ok", false}, {"code", "METHOD_NOT_ALLOWED"}, {"message", "POST required"}});
        return;
    }

    QByteArray authorizationHeader;
    qsizetype contentLength = 0;
    for (qsizetype i = 1; i < headerLines.size(); ++i) {
        const QByteArray line = headerLines[i].trimmed();
        const qsizetype headerSeparator = line.indexOf(':');
        if (headerSeparator < 0) continue;
        const QByteArray headerName = line.left(headerSeparator).trimmed().toLower();
        const QByteArray headerValue = line.mid(headerSeparator + 1).trimmed();
        if (headerName == "authorization") authorizationHeader = headerValue;
        if (headerName == "content-length") contentLength = headerValue.toLongLong();
    }
    if (authorizationHeader != QByteArray("Bearer ") + token_.toLatin1()) {
        sendHttpResponse(socket, 401, {{"ok", false}, {"code", "UNAUTHORIZED"}, {"message", "invalid local bridge token"}});
        return;
    }

    const QString requestPath = QString::fromUtf8(requestLine[1]);
    const QString toolPrefix = QStringLiteral("/tool/");
    const bool nativeRequest = requestPath == QLatin1String("/native");
    if (!nativeRequest && !requestPath.startsWith(toolPrefix)) {
        sendHttpResponse(socket, 404, {{"ok", false}, {"code", "NOT_FOUND"}, {"message", "unknown endpoint"}});
        return;
    }
    const qsizetype bodyLimit = nativeRequest ? kNativeBodyLimit : kToolBodyLimit;
    if (contentLength < 0 || contentLength > bodyLimit) {
        const QJsonObject rejection{{"ok", false},
                                    {"code", "REQUEST_TOO_LARGE"},
                                    {"message", QStringLiteral("request body exceeds %1 bytes").arg(bodyLimit)}};
        const qsizetype bodyStartHere = headerEnd + 4;
        const qint64 alreadyRead = qMax<qint64>(0, buffer.size() - bodyStartHere);
        const qint64 remaining = contentLength - alreadyRead;
        if (contentLength <= kNativeBodyLimit * 2 && remaining > 0) {
            // 体量还在可丢弃的范围：把剩下的读完，客户端才能收到明确的 413。
            socket->setProperty("requestBuffer", QByteArray{});
            socket->setProperty("discardLeft", remaining);
            socket->setProperty("rejectBody", rejection);
            return;
        }
        sendHttpResponse(socket, 413, rejection);
        return;
    }
    const qsizetype bodyStart = headerEnd + 4;
    if (buffer.size() - bodyStart < contentLength) return;

    const QByteArray body = buffer.mid(bodyStart, contentLength);
    if (nativeRequest) {
        dispatchNative(socket, body);
        return;
    }

    QJsonParseError parseError;
    const QJsonDocument inputDocument = QJsonDocument::fromJson(body, &parseError);
    if (parseError.error != QJsonParseError::NoError || !inputDocument.isObject()) {
        sendHttpResponse(socket, 400, {{"ok", false}, {"code", "INVALID_ARGUMENT"}, {"message", "tool input must be a JSON object"}});
        return;
    }
    const QJsonObject result = toolHandler_(requestPath.mid(toolPrefix.size()), inputDocument.object());
    sendHttpResponse(socket, result.value("ok").toBool() ? 200 : 400, result);
}

void LocalBridge::dispatchNative(QTcpSocket *socket, const QByteArray &body) {
    if (!nativeHandler_) {
        sendHttpResponse(socket, 400,
                         {{"ok", false}, {"code", "NOT_CONFIGURED"}, {"message", "local bridge does not serve native operations"}});
        return;
    }
    QJsonParseError parseError;
    const QJsonDocument document = QJsonDocument::fromJson(body, &parseError);
    if (parseError.error != QJsonParseError::NoError || !document.isObject()) {
        sendHttpResponse(socket, 400,
                         {{"ok", false}, {"code", "INVALID_ARGUMENT"}, {"message", "native request must be a JSON object"}});
        return;
    }
    // 连接可能在操作跑完之前就断了（MCP 进程退出）：用 QPointer 保住 socket 的生命周期判断。
    QPointer<QTcpSocket> guard(socket);
    auto respond = [this, guard](const QJsonObject &envelope) {
        if (guard.isNull()) return;
        sendHttpResponse(guard.data(), envelope.value(QStringLiteral("ok")).toBool() ? 200 : 400, envelope);
    };
    nativeHandler_(document.object(), respond);
}

void LocalBridge::sendHttpResponse(QTcpSocket *socket, int statusCode, const QJsonObject &body) {
    if (socket->property("responded").toBool()) return;
    socket->setProperty("responded", true);
    const QByteArray payload = QJsonDocument(body).toJson(QJsonDocument::Compact);
    QByteArray response = "HTTP/1.1 " + QByteArray::number(statusCode) + " " + reasonPhrase(statusCode) + "\r\n";
    response += "Content-Type: application/json\r\n";
    response += "Content-Length: " + QByteArray::number(payload.size()) + "\r\n";
    response += "Connection: close\r\n\r\n" + payload;
    socket->write(response);
    socket->disconnectFromHost();
}
