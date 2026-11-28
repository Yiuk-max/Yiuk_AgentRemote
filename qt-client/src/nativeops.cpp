#include "nativeops.h"

#include <QJsonDocument>
#include <QRunnable>
#include <QThreadPool>
#include <QTimer>

// 一次待执行的原生操作。定义在文件作用域（不是匿名命名空间），
// 这样 worker 与调度器看到的是同一个类型。
struct PendingOperation {
    NativeOperationDispatcher::Handler handler;
    QJsonObject arguments;
    QString sessionId;
    QString requestId;
    // 与调度器 Job 共享：处理器可以周期性检查它来决定是否提前退出。
    std::shared_ptr<std::atomic_bool> cancelled;
};

namespace {

// OperationTask 把一次操作搬到线程池；结果用 QueuedConnection 送回调度器所在线程（主线程）。
class OperationTask final : public QRunnable {
public:
    OperationTask(NativeOperationDispatcher *dispatcher, std::shared_ptr<PendingOperation> pending)
        : dispatcher_(dispatcher), pending_(std::move(pending)) {
        setAutoDelete(true);
    }

    void run() override {
        QJsonObject envelope;
        try {
            envelope = pending_->handler(pending_->sessionId, pending_->arguments, pending_->cancelled.get());
        } catch (const std::exception &error) {
            envelope = NativeOperationDispatcher::envelopeError(
                QStringLiteral("NATIVE_OPERATION_FAILED"), QString::fromUtf8(error.what()));
        } catch (...) {
            envelope = NativeOperationDispatcher::envelopeError(QStringLiteral("NATIVE_OPERATION_FAILED"),
                                                               QStringLiteral("原生操作抛出未知异常"));
        }
        // 超时/取消可能已经先结算过：finishFromWorker 会发现 job 不在表里而丢弃这次结果。
        QMetaObject::invokeMethod(
            dispatcher_,
            [dispatcher = dispatcher_, sessionId = pending_->sessionId, requestId = pending_->requestId,
             envelope]() { dispatcher->finishFromWorker(sessionId, requestId, envelope); },
            Qt::QueuedConnection);
    }

private:
    NativeOperationDispatcher *dispatcher_;
    std::shared_ptr<PendingOperation> pending_;
};

}  // namespace

NativeOperationDispatcher::NativeOperationDispatcher(QObject *parent) : QObject(parent) {
    pool_ = new QThreadPool(this);
    // 设备是用户正在用的机器：同时只跑 2 个原生操作，抓屏/OCR 不会把 CPU 打满。
    pool_->setMaxThreadCount(2);
}

NativeOperationDispatcher::~NativeOperationDispatcher() {
    cancelAll();
    pool_->clear();
    // 视觉/抓屏都是百毫秒级；给有限等待，避免退出时卡在某个 handler 上。
    pool_->waitForDone(3000);
}

void NativeOperationDispatcher::registerOperation(const QString &name, Handler handler) {
    if (name.isEmpty() || !handler) return;
    handlers_.insert(name, std::move(handler));
}

QStringList NativeOperationDispatcher::capabilities() const {
    QStringList names = handlers_.keys();
    names.sort();
    return names;
}

bool NativeOperationDispatcher::supports(const QString &operation) const {
    return handlers_.contains(operation);
}

bool NativeOperationDispatcher::start(const QString &sessionId, const QString &requestId,
                                      const QString &operation, const QJsonObject &arguments,
                                      int timeoutSeconds) {
    if (sessionId.isEmpty() || requestId.isEmpty() || operation.isEmpty()) return false;
    if (knownRequests_.contains(requestId) || jobs_.contains(requestId)) return false;

    const auto handler = handlers_.value(operation);
    if (!handler) {
        // 未知操作也用错误信封回应，而不是静默忽略：让 Agent 看到明确原因。
        QTimer::singleShot(0, this, [this, sessionId, requestId, operation]() {
            emit finished(sessionId, requestId, QStringLiteral("succeeded"),
                          envelopeError(QStringLiteral("UNSUPPORTED_OPERATION"),
                                        QStringLiteral("客户端未注册操作 %1").arg(operation)));
        });
        return true;
    }

    auto job = std::make_shared<Job>();
    job->sessionId = sessionId;
    job->requestId = requestId;
    job->operation = operation;
    jobs_.insert(requestId, job);

    if (timeoutSeconds > 0) {
        auto *timer = new QTimer(this);
        timer->setSingleShot(true);
        job->timer = timer;
        connect(timer, &QTimer::timeout, this, [this, job, timeoutSeconds]() {
            // 超时先于结果：complete() 把 job 从表里摘掉，之后到达的 handler 结果被丢弃。
            job->cancelled->store(true);
            complete(job, QStringLiteral("timed_out"),
                     envelopeError(QStringLiteral("NATIVE_TIMEOUT"),
                                   QStringLiteral("操作在 %1 秒内没有完成").arg(timeoutSeconds)));
        });
        timer->start(timeoutSeconds * 1000);
    }

    auto pending = std::make_shared<PendingOperation>();
    pending->handler = handler;
    pending->arguments = arguments;
    pending->sessionId = sessionId;
    pending->requestId = requestId;
    pending->cancelled = job->cancelled;

    emit activity(sessionId, QStringLiteral("开始原生操作 %1").arg(operation));
    // 线程池里只拿 operation 名与参数；取消标记在 Job 上，complete() 结算后 handler
    // 的返回会被丢弃，因此不需要把 Job 传进 worker（避免生命周期纠缠）。
    pool_->start(new OperationTask(this, pending));
    return true;
}

void NativeOperationDispatcher::cancel(const QString &requestId) {
    const auto job = jobs_.value(requestId);
    if (!job) return;
    job->cancelled->store(true);
    complete(job, QStringLiteral("cancelled"),
             envelopeError(QStringLiteral("CANCELLED"), QStringLiteral("操作已被取消")));
}

void NativeOperationDispatcher::cancelSession(const QString &sessionId) {
    const auto requestIds = jobs_.keys();
    for (const QString &requestId : requestIds) {
        const auto job = jobs_.value(requestId);
        if (job && job->sessionId == sessionId) cancel(requestId);
    }
}

void NativeOperationDispatcher::cancelAll() {
    const auto requestIds = jobs_.keys();
    for (const QString &requestId : requestIds) cancel(requestId);
}

int NativeOperationDispatcher::activeCount() const { return jobs_.size(); }

void NativeOperationDispatcher::finishFromWorker(const QString &sessionId, const QString &requestId,
                                                const QJsonObject &envelope) {
    const auto job = jobs_.value(requestId);
    if (!job || job->sessionId != sessionId) return;  // 已被超时/取消结算
    complete(job, QStringLiteral("succeeded"), envelope);
}

void NativeOperationDispatcher::complete(const JobPtr &job, const QString &status, const QJsonObject &envelope) {
    if (!job) return;
    // 同一个 request 只结算一次：先到者赢。
    if (jobs_.value(job->requestId) != job) return;
    jobs_.remove(job->requestId);
    // 只在最近一段时间内挡重复请求；表满就整体清空，避免长期运行无限增长。
    if (knownRequests_.size() > 512) knownRequests_.clear();
    knownRequests_.insert(job->requestId);
    if (job->timer) {
        job->timer->stop();
        job->timer->deleteLater();
        job->timer = nullptr;
    }
    job->cancelled->store(true);

    QJsonObject payload = envelope;
    // 信封超限就换成错误：宁可让 Agent 看到明确失败，也不要发一个必然被截断的帧。
    const int size = QJsonDocument(payload).toJson(QJsonDocument::Compact).size();
    if (size > kMaxEnvelopeBytes) {
        payload = envelopeError(QStringLiteral("NATIVE_RESULT_TOO_LARGE"),
                                QStringLiteral("结果 %1 字节，超过客户端上限 %2 字节").arg(size).arg(kMaxEnvelopeBytes),
                                QStringLiteral("缩小 region 或减少 limit 后重试"));
    }
    emit activity(job->sessionId, QStringLiteral("原生操作 %1：%2").arg(job->operation, status));
    emit finished(job->sessionId, job->requestId, status, payload);
}

QJsonObject NativeOperationDispatcher::envelopeOk(const QJsonObject &data) {
    return QJsonObject{{QStringLiteral("ok"), true}, {QStringLiteral("data"), data}};
}

QJsonObject NativeOperationDispatcher::envelopeError(const QString &code, const QString &message,
                                                     const QString &hint) {
    QJsonObject envelope{
        {QStringLiteral("ok"), false},
        {QStringLiteral("code"), code},
        {QStringLiteral("message"), message},
    };
    if (!hint.isEmpty()) envelope.insert(QStringLiteral("hint"), hint);
    return envelope;
}
