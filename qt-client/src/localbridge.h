#pragma once

#include <QJsonObject>
#include <QTcpServer>
#include <functional>

class QTcpSocket;

// 本机 MCP 桥：loopback + Bearer token，只服务"没有平台"的本地后端。
//
// 两条通道：
//   * POST /tool/<name>：同步工具（列表/会话/命令/文件/询问），handler 直接返回结果；
//   * POST /native：原生操作（抓屏 / 鼠标 / 视觉），**异步**回包——抓屏 30ms、
//     OCR 300ms、等目标出现可以到 20s，同步模型会把 GUI 线程按住。
// 会话与权限仍由客户端校验（runLocalNative），这里只负责传输与边界。
class LocalBridge final : public QTcpServer {
public:
    using ToolHandler = std::function<QJsonObject(const QString &, const QJsonObject &)>;
    using NativeResponder = std::function<void(const QJsonObject &envelope)>;
    using NativeHandler = std::function<void(const QJsonObject &request, NativeResponder respond)>;

    explicit LocalBridge(ToolHandler toolHandler, QObject *parent = nullptr);
    // 不设置时 /native 返回 NOT_CONFIGURED，旧客户端行为不变。
    void setNativeHandler(NativeHandler handler);
    QString token() const;
    QString endpoint() const;
    bool start(QString *error = nullptr);

    // 请求体上限：普通工具 32 KiB；原生操作要与平台侧的 native_argument_max_bytes(128 KiB)
    // 对齐，再多留一帧余量给根对象字段。
    static constexpr qsizetype kToolBodyLimit = 32 << 10;
    static constexpr qsizetype kNativeBodyLimit = (128 << 10) + (4 << 10);

protected:
    void incomingConnection(qintptr socketDescriptor) override;

private:
    void readAndDispatchRequest(QTcpSocket *socket);
    void dispatchNative(QTcpSocket *socket, const QByteArray &body);
    void sendHttpResponse(QTcpSocket *socket, int statusCode, const QJsonObject &body);

    ToolHandler toolHandler_;
    NativeHandler nativeHandler_;
    QString token_;
};
