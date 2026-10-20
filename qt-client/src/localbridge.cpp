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

LocalBridge::LocalBridge(Handler handler, QObject *parent)
    : QTcpServer(parent), handler_(std::move(handler)) {
    const QByteArray seed = QByteArray::number(QRandomGenerator::global()->generate64()) +
        QByteArray::number(QRandomGenerator::global()->generate64());
    token_ = QString::fromLatin1(QCryptographicHash::hash(seed, QCryptographicHash::Sha256).toHex());
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
    connect(socket, &QTcpSocket::readyRead, this, [this, socket] { readRequest(socket); });
    connect(socket, &QTcpSocket::disconnected, socket, &QObject::deleteLater);
    QTimer::singleShot(5000, socket, [socket] {
        if (socket->state() == QAbstractSocket::ConnectedState && !socket->property("responded").toBool())
            socket->disconnectFromHost();
    });
}

void LocalBridge::readRequest(QTcpSocket *socket) {
    QByteArray buffer = socket->property("requestBuffer").toByteArray();
    buffer += socket->readAll();
    if (buffer.size() > (64 << 10)) {
        respond(socket, 413, {{"ok", false}, {"code", "REQUEST_TOO_LARGE"}, {"message", "request exceeds 64 KiB"}});
        return;
    }
    socket->setProperty("requestBuffer", buffer);
    const qsizetype headerEnd = buffer.indexOf("\r\n\r\n");
    if (headerEnd < 0) return;

    const QList<QByteArray> lines = buffer.left(headerEnd).split('\n');
    if (lines.isEmpty()) {
        respond(socket, 400, {{"ok", false}, {"code", "BAD_REQUEST"}, {"message", "invalid HTTP request"}});
        return;
    }
    const QList<QByteArray> requestLine = lines.first().trimmed().split(' ');
    if (requestLine.size() != 3 || requestLine[0] != "POST") {
        respond(socket, 405, {{"ok", false}, {"code", "METHOD_NOT_ALLOWED"}, {"message", "POST required"}});
        return;
    }

    QByteArray auth;
    qsizetype contentLength = 0;
    for (qsizetype i = 1; i < lines.size(); ++i) {
        const QByteArray line = lines[i].trimmed();
        const qsizetype colon = line.indexOf(':');
        if (colon < 0) continue;
        const QByteArray key = line.left(colon).trimmed().toLower();
        const QByteArray value = line.mid(colon + 1).trimmed();
        if (key == "authorization") auth = value;
        if (key == "content-length") contentLength = value.toLongLong();
    }
    if (auth != QByteArray("Bearer ") + token_.toLatin1()) {
        respond(socket, 401, {{"ok", false}, {"code", "UNAUTHORIZED"}, {"message", "invalid local bridge token"}});
        return;
    }
    if (contentLength < 0 || contentLength > (32 << 10)) {
        respond(socket, 413, {{"ok", false}, {"code", "REQUEST_TOO_LARGE"}, {"message", "tool input exceeds 32 KiB"}});
        return;
    }
    const qsizetype bodyStart = headerEnd + 4;
    if (buffer.size() - bodyStart < contentLength) return;

    const QString path = QString::fromUtf8(requestLine[1]);
    const QString prefix = QStringLiteral("/tool/");
    if (!path.startsWith(prefix)) {
        respond(socket, 404, {{"ok", false}, {"code", "NOT_FOUND"}, {"message", "unknown endpoint"}});
        return;
    }
    QJsonParseError parseError;
    const QJsonDocument input = QJsonDocument::fromJson(buffer.mid(bodyStart, contentLength), &parseError);
    if (parseError.error != QJsonParseError::NoError || !input.isObject()) {
        respond(socket, 400, {{"ok", false}, {"code", "INVALID_ARGUMENT"}, {"message", "tool input must be a JSON object"}});
        return;
    }
    const QString toolName = path.mid(prefix.size());
    const QJsonObject result = handler_(toolName, input.object());
    respond(socket, result.value("ok").toBool() ? 200 : 400, result);
}

void LocalBridge::respond(QTcpSocket *socket, int status, const QJsonObject &body) {
    if (socket->property("responded").toBool()) return;
    socket->setProperty("responded", true);
    const QByteArray payload = QJsonDocument(body).toJson(QJsonDocument::Compact);
    const QByteArray reason = status == 200 ? "OK" : status == 401 ? "Unauthorized" :
        status == 404 ? "Not Found" : status == 405 ? "Method Not Allowed" : "Bad Request";
    QByteArray response = "HTTP/1.1 " + QByteArray::number(status) + " " + reason + "\r\n";
    response += "Content-Type: application/json\r\n";
    response += "Content-Length: " + QByteArray::number(payload.size()) + "\r\n";
    response += "Connection: close\r\n\r\n" + payload;
    socket->write(response);
    socket->disconnectFromHost();
}
