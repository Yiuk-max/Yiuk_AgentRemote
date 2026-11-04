#pragma once

#include <QJsonObject>
#include <QTcpServer>
#include <functional>

class QTcpSocket;

class LocalBridge final : public QTcpServer {
public:
    using ToolHandler = std::function<QJsonObject(const QString &, const QJsonObject &)>;

    explicit LocalBridge(ToolHandler toolHandler, QObject *parent = nullptr);
    QString token() const;
    QString endpoint() const;
    bool start(QString *error = nullptr);

protected:
    void incomingConnection(qintptr socketDescriptor) override;

private:
    void readAndDispatchRequest(QTcpSocket *socket);
    void sendHttpResponse(QTcpSocket *socket, int statusCode, const QJsonObject &body);

    ToolHandler toolHandler_;
    QString token_;
};
