#pragma once

#include <QAbstractSocket>
#include <QObject>
#include <QJsonArray>
#include <QJsonObject>
#include <QStringList>
#include <QTimer>

class QTcpSocket;

// PlatformClient 使用 Yiuk 平台的带帧 JSON 协议进行通信
//
// 同一对象既可作为本地设备（register_device、批准或拒绝会话、执行命令），也可作为操作端（request_session）
// 两种角色共用一个经过认证的连接，因为平台会将会话绑定到创建该会话的连接
class PlatformClient final : public QObject {
    Q_OBJECT
public:
    struct Config {
        QString address;            // 主机地址和端口
        QString playerPassword;     // 平台玩家密码
        QString deviceId;           // 本地设备 ID（仅数字）
        QString deviceName;         // 显示名称
        QString temporaryPassword;  // 32 位临时密码
        int passwordVersion = 1;
        QString mode;               // "direct" 或 "approval"
        int ttlSeconds = 24 * 60 * 60;
        bool tls = true;
        bool allowInsecure = false; // 仅用于开发：跳过证书验证
        QString caFile;
        // 服务器证书的 SHA-256 指纹，以十六进制编码
        // 设置后，客户端只接受该证书，而不依赖公共 CA
        // 这样使用自签名证书时无需关闭对任意证书的验证
        QString pinnedFingerprint;
        // 大多数 TUN 适配器只提供 IPv4 路由
        // 同时避免服务器使用 IP 地址配置时，IPv6 路由绕过 VPN
        bool forceIPv4 = true;
        // 某些 Windows Schannel 版本与特定 Go 服务端配置进行 TLS 1.3 握手时会失败，但 TLS 1.2 可以成功
        // 设置此选项后，连接将固定使用 TLS 1.2
        bool forceTls12 = false;
        bool enabled = true;
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

    // 设备端操作
    void approveSession(const QString &sessionId);
    void rejectSession(const QString &sessionId);
    void sendCommandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                           const QString &data, quint64 sequence);
    void sendCommandResult(const QString &sessionId, const QString &requestId, const QString &status,
                           const QJsonValue &exitCode, bool resultKnown, bool truncated);
    void sendFileChunk(const QString &sessionId, const QString &requestId, quint64 sequence,
                       const QByteArray &data, bool eof);
    void sendFileDownloadResult(const QString &sessionId, const QString &requestId, const QString &status,
                                const QString &fileName, qint64 fileSize, const QString &sha256,
                                bool resultKnown);
    void sendFileUploadResult(const QString &sessionId, const QString &requestId, const QString &status,
                              const QString &remotePath, const QString &fileName, qint64 fileSize,
                              const QString &sha256, bool resultKnown);
    void updateTemporaryPassword(const QString &password, int version, const QString &mode, int ttlSeconds);
    void closeSession(const QString &sessionId);
    void cancelCommand(const QString &sessionId, const QString &requestId);

    // 操作端操作
    void listDevices(const QString &requestId);
    void requestSession(const QString &deviceId, const QString &purpose,
                        const QStringList &permissions, const QString &temporaryPassword);
    void executeCommand(const QString &sessionId, const QString &requestId, const QString &command,
                        const QString &workingDirectory, int timeoutSeconds);

signals:
    void stateChanged(const QString &state);
    void registered(const QJsonObject &device);
    void deviceListReceived(const QJsonArray &devices, const QString &requestId);
    void requestFailed(const QString &requestId, const QString &code, const QString &message);
    void sessionRequested(const QJsonObject &session);
    void sessionUpdated(const QJsonObject &session);
    void sessionCreated(const QJsonObject &session);
    void commandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                          const QString &workingDirectory, int timeoutSeconds);
    void commandOutput(const QString &sessionId, const QString &requestId, const QString &stream,
                       const QString &data, quint64 sequence, bool truncated);
    void commandResult(const QString &sessionId, const QString &requestId, const QString &status,
                       const QJsonValue &exitCode, bool resultKnown, bool truncated);
    void fileDownloadRequested(const QString &sessionId, const QString &requestId, const QString &remotePath);
    void fileDownloadCanceled(const QString &sessionId, const QString &requestId);
    void fileUploadRequested(const QString &sessionId, const QString &requestId, const QString &remotePath,
                             const QString &fileName, qint64 fileSize, const QString &sha256, bool overwrite);
    void fileUploadChunkReceived(const QString &sessionId, const QString &requestId, quint64 sequence,
                                 const QString &data, bool eof);
    void fileUploadCanceled(const QString &sessionId, const QString &requestId);
    void cancelRequested(const QString &sessionId, const QString &requestId);
    void closeRequested(const QString &sessionId);
    void temporaryPasswordUpdated(const QString &password, int version, qint64 expiresAt);
    void credentialsRenewed(const QString &password, int version);
    void platformError(const QString &code, const QString &message);
    void connectionError(const QString &message);
    // 服务器证书无法验证时发出
    // 用户必须明确确认信任该指纹后，客户端才会连接
    void certificateUntrusted(const QString &fingerprint, const QString &subject,
                              const QString &details, bool changed);

private slots:
    void onConnected();
    void onEncrypted();
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
    void retryWithoutProxy();
    void beginSession();
    void rejectCertificate(const QString &fingerprint, const QString &subject, const QString &details, bool changed);

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
    bool rotatePasswordOnFirstRegistration_ = true;
    QString pendingTemporaryPassword_;
    bool proxyFallbackAttempted_ = false;
    bool forceDirectConnection_ = false;
    bool usingProxy_ = false;
    // 当前连接已报告 TLS 验证失败时设置，避免对端中止连接后用笼统的“远程主机已关闭”替代真实原因
    // 对端证书无法通过平台 CA 存储验证时设置
    // 在握手完成后作出判断，确保 Schannel 和 OpenSSL 后端的结果一致
    bool sslErrorsSeen_ = false;
    bool tlsErrorReported_ = false;
    int reconnectAttempts_ = 0;
};