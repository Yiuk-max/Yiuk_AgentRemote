#include "localbridge.h"

#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QHostAddress>
#include <QJsonDocument>
#include <QJsonParseError>
#include <QRandomGenerator>
#include <QTcpSocket>
#include <QTimer>

LocalBridge::LocalBridge(ToolHandler toolHandler, QObject *parent)
    : QTcpServer(parent), toolHandler_(std::move(toolHandler)) {
    const QByteArray tokenSeed = QByteArray::number(QRandomGenerator::global()->generate64()) +
        QByteArray::number(QRandomGenerator::global()->generate64());
    token_ = QString::fromLatin1(QCryptographicHash::hash(tokenSeed, QCryptographicHash::Sha256).toHex());
}

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
    QTimer::singleShot(5000, socket, [socket] {
        if (socket->state() == QAbstractSocket::ConnectedState && !socket->property("responded").toBool())
            socket->disconnectFromHost();
    });
}

void LocalBridge::readAndDispatchRequest(QTcpSocket *socket) {
    QByteArray buffer = socket->property("requestBuffer").toByteArray();
    buffer += socket->readAll();
    if (buffer.size() > (64 << 10)) {
        sendHttpResponse(socket, 413, {{"ok", false}, {"code", "REQUEST_TOO_LARGE"}, {"message", "request exceeds 64 KiB"}});
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
    if (contentLength < 0 || contentLength > (32 << 10)) {
        sendHttpResponse(socket, 413, {{"ok", false}, {"code", "REQUEST_TOO_LARGE"}, {"message", "tool input exceeds 32 KiB"}});
        return;
    }
    const qsizetype bodyStart = headerEnd + 4;
    if (buffer.size() - bodyStart < contentLength) return;

    const QString requestPath = QString::fromUtf8(requestLine[1]);
    const QString prefix = QStringLiteral("/tool/");
    if (!requestPath.startsWith(prefix)) {
        sendHttpResponse(socket, 404, {{"ok", false}, {"code", "NOT_FOUND"}, {"message", "unknown endpoint"}});
        return;
    }
    QJsonParseError parseError;
    const QJsonDocument inputDocument = QJsonDocument::fromJson(buffer.mid(bodyStart, contentLength), &parseError);
    if (parseError.error != QJsonParseError::NoError || !inputDocument.isObject()) {
        sendHttpResponse(socket, 400, {{"ok", false}, {"code", "INVALID_ARGUMENT"}, {"message", "tool input must be a JSON object"}});
        return;
    }
    const QString toolName = requestPath.mid(prefix.size());
    const QJsonObject result = toolHandler_(toolName, inputDocument.object());
    sendHttpResponse(socket, result.value("ok").toBool() ? 200 : 400, result);
}

void LocalBridge::sendHttpResponse(QTcpSocket *socket, int statusCode, const QJsonObject &body) {
    if (socket->property("responded").toBool()) return;
    socket->setProperty("responded", true);
    const QByteArray payload = QJsonDocument(body).toJson(QJsonDocument::Compact);
    const QByteArray reasonPhrase = statusCode == 200 ? "OK" : statusCode == 401 ? "Unauthorized" :
        statusCode == 404 ? "Not Found" : statusCode == 405 ? "Method Not Allowed" : "Bad Request";
    QByteArray response = "HTTP/1.1 " + QByteArray::number(statusCode) + " " + reasonPhrase + "\r\n";
    response += "Content-Type: application/json\r\n";
    response += "Content-Length: " + QByteArray::number(payload.size()) + "\r\n";
    response += "Connection: close\r\n\r\n" + payload;
    socket->write(response);
    socket->disconnectFromHost();
}
