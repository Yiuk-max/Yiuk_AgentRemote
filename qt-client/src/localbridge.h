#pragma once

#include <QJsonObject>
#include <QTcpServer>
#include <functional>

class QTcpSocket;

class LocalBridge final : public QTcpServer {
public:
    using Handler = std::function<QJsonObject(const QString &, const QJsonObject &)>;

    explicit LocalBridge(Handler handler, QObject *parent = nullptr);
    QString token() const;
    QString endpoint() const;
    bool start(QString *error = nullptr);

protected:
    void incomingConnection(qintptr socketDescriptor) override;

private:
    void readRequest(QTcpSocket *socket);
    void respond(QTcpSocket *socket, int status, const QJsonObject &body);

    Handler handler_;
    QString token_;
};
