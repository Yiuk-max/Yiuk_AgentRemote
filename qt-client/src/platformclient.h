#pragma once

#include <QAbstractSocket>
#include <QObject>
#include <QJsonObject>
#include <QStringList>
#include <QTimer>

class QTcpSocket;

// PlatformClient speaks the Yiuk platform framed-JSON protocol.
//
// The same object can act as the local device (register_device, approve/reject
// sessions, execute commands) and as an operator (request_session). A single
// authenticated connection is shared because the platform binds a session to
// the connection that created it.
class PlatformClient final : public QObject {
    Q_OBJECT
public:
    struct Config {
        QString address;            // host:port
        QString playerPassword;     // platform player password
        QString deviceId;           // local device id (digits only)
        QString deviceName;         // display name
        QString temporaryPassword;  // 32-character temporary password
        int passwordVersion = 1;
        QString mode;               // "direct" or "approval"
        int ttlSeconds = 600;
        bool tls = true;
        bool allowInsecure = false; // dev only: skip certificate verification
        QString caFile;
        bool enabled = false;
    };

    explicit PlatformClient(QObject *parent = nullptr);

    void setConfig(const Config &config);
    Config config() const { return config_; }

    void connectToPlatform();
    void disconnectFromPlatform();
    bool isConnected() const;
    bool isRegistered() const { return registered_; }
    QString connectionId() const { return connectionId_; }
    QString state() const { return state_; }

    // Device actions.
    void approveSession(const QString &sessionId);
    void rejectSession(const QString &sessionId);
    void sendCommandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                           const QString &data, quint64 sequence);
    void sendCommandResult(const QString &sessionId, const QString &requestId, const QString &status,
                           const QJsonValue &exitCode, bool resultKnown, bool truncated);
    void updateTemporaryPassword(const QString &password, int version, const QString &mode, int ttlSeconds);
    void closeSession(const QString &sessionId);
    void cancelCommand(const QString &sessionId, const QString &requestId);

    // Operator actions.
    void requestSession(const QString &deviceId, const QString &purpose,
                        const QStringList &permissions, const QString &temporaryPassword);
    void executeCommand(const QString &sessionId, const QString &requestId, const QString &command,
                        const QString &workingDirectory, int timeoutSeconds);

signals:
    void stateChanged(const QString &state);
    void registered(const QJsonObject &device);
    void sessionRequested(const QJsonObject &session);
    void sessionUpdated(const QJsonObject &session);
    void sessionCreated(const QJsonObject &session);
    void commandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                          const QString &workingDirectory, int timeoutSeconds);
    void commandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                       const QString &data, quint64 sequence, bool truncated);
    void commandResult(const QString &sessionId, const QString &requestId, const QString &status,
                       const QJsonValue &exitCode, bool resultKnown, bool truncated);
    void cancelRequested(const QString &sessionId, const QString &requestId);
    void closeRequested(const QString &sessionId);
    void temporaryPasswordUpdated(int version, qint64 expiresAt);
    void platformError(const QString &code, const QString &message);
    void connectionError(const QString &message);

private slots:
    void onConnected();
    void onReadyRead();
    void onDisconnected();
    void onSocketError(QAbstractSocket::SocketError error);
    void onHeartbeat();
    void onReconnect();

private:
    void send(const QJsonObject &message);
    void handleMessage(const QJsonObject &message);
    void setState(const QString &state);
    void scheduleReconnect();

    QTcpSocket *socket_ = nullptr;
    QTimer heartbeatTimer_;
    QTimer reconnectTimer_;
    Config config_;
    QByteArray buffer_;
    QString connectionId_;
    QString state_ = QStringLiteral("disconnected");
    bool manualDisconnect_ = false;
    bool authenticated_ = false;
    bool registered_ = false;
    int reconnectAttempts_ = 0;
};