#pragma once

#include <QHash>
#include <QJsonObject>
#include <QObject>
#include <QSet>
#include <QString>
#include <QStringList>

#include <atomic>
#include <functional>
#include <memory>

class QThreadPool;
class QTimer;

// NativeOperationDispatcher 在客户端内部执行“原生操作”（抓屏、鼠标、视觉定位）。
//
// 与 execute_command 的区别：这里不接受脚本，只接受注册过的操作名 + 结构化参数；
// 平台与 MCP 各自还有一层白名单，客户端这一层是最后一道，避免被攻破的连接
// 借原生通道做任意事情。
//
// 线程模型：
//   * start() 在主线程调用，参数在这里校验、登记、启动定时器；
//   * handler 跑在独立的线程池里（不阻塞 GUI 与心跳）；
//   * 结果通过 finished() 信号回到主线程，由 MainWindow 转成平台帧。
//
// 取消：cancel() 只置位原子标记并立即回 cancelled；处理器必须自己检查标记
// （长循环要分段检查），这与“平台不再等待”的语义一致。
class NativeOperationDispatcher final : public QObject {
    Q_OBJECT
public:
    // handler 返回业务信封里的 data；失败时用 fail()/failFrom 构造错误。
    // cancelled 由调度器持有，处理器应周期性检查。
    // sessionId 是平台下发的会话 ID：抓屏类操作要靠它做会话级互斥与快照登记。
    using Handler = std::function<QJsonObject(const QString &sessionId, const QJsonObject &arguments,
                                             const std::atomic_bool *cancelled)>;

    explicit NativeOperationDispatcher(QObject *parent = nullptr);
    ~NativeOperationDispatcher() override;

    void registerOperation(const QString &name, Handler handler);
    QStringList capabilities() const;
    bool supports(const QString &operation) const;

    // start 返回 false 表示参数/操作名不合法或同名请求已在执行（不会发出 finished）。
    bool start(const QString &sessionId, const QString &requestId, const QString &operation,
               const QJsonObject &arguments, int timeoutSeconds);

    // 由工作线程回到主线程的结算入口（handler 结束后调用；已被超时/取消结算则丢弃）。
    void finishFromWorker(const QString &sessionId, const QString &requestId, const QJsonObject &envelope);

    void cancel(const QString &requestId);
    void cancelSession(const QString &sessionId);
    void cancelAll();
    int activeCount() const;

    // 结果信封的大小上限：平台单帧 256 KiB，这里留足余量并保证不会把
    // 图片/长文本塞进 JSON。
    static constexpr int kMaxEnvelopeBytes = 32 * 1024;

    static QJsonObject envelopeOk(const QJsonObject &data);
    // data 是可选的现场信息（例如 click_target 的候选列表与得分）：失败也需要把
    // "为什么没点"告诉 Agent。体积计入 kMaxEnvelopeBytes，所以只能放摘要，不能放图片。
    static QJsonObject envelopeError(const QString &code, const QString &message, const QString &hint = QString(),
                                     const QJsonObject &data = QJsonObject());

signals:
    void finished(const QString &sessionId, const QString &requestId, const QString &status,
                  const QJsonObject &envelope);
    void activity(const QString &sessionId, const QString &summary);

private:
    struct Job {
        QString sessionId;
        QString requestId;
        QString operation;
        // 与 worker 共享的取消标记：超时/取消立刻能被处理器看到。
        std::shared_ptr<std::atomic_bool> cancelled = std::make_shared<std::atomic_bool>(false);
        QTimer *timer = nullptr;
    };

    using JobPtr = std::shared_ptr<Job>;

    void complete(const JobPtr &job, const QString &status, const QJsonObject &envelope);

    QHash<QString, Handler> handlers_;
    QHash<QString, JobPtr> jobs_;
    QSet<QString> knownRequests_;  // 已完成的 request_id，防止平台重发导致重复执行
    QThreadPool *pool_ = nullptr;
};
