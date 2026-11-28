#include "visionservice.h"

#include "input/inputcontroller.h"

#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QImage>
#include <QThread>
#include <QJsonArray>
#include <QJsonObject>
#include <QtMath>
#include <QStandardPaths>

namespace yiuk::vision {

namespace {

// 只覆盖本次 acquire 的 RAII：任何提前返回都会归还会话门。
struct LimiterGuard {
    SessionLimiter *limiter;
    QString sessionId;
    ~LimiterGuard() { limiter->release(sessionId); }
};

QString normalizedFormat(const QString &format) {
    if (format.compare(QLatin1String("jpeg"), Qt::CaseInsensitive) == 0 ||
        format.compare(QLatin1String("jpg"), Qt::CaseInsensitive) == 0)
        return QStringLiteral("jpeg");
    return QStringLiteral("png");
}

}  // namespace

VisionService::VisionService(QObject *parent) : QObject(parent), layoutVersion_(currentLayoutVersion()) {}

VisionService::DeviceLock::DeviceLock(VisionService &service, const std::atomic_bool *cancelled, QString *code,
                                      QString *message)
    : service_(&service) {
    QString error;
    if (service_->deviceGate_.enter(cancelled, &error)) {
        held_ = true;
        return;
    }
    // 排队期间被取消：与抓屏取消的语义保持一致（CANCELLED，而不是通用失败）。
    if (code) *code = QStringLiteral("CANCELLED");
    if (message) *message = error;
    service_ = nullptr;
}

VisionService::DeviceLock::~DeviceLock() {
    if (held_ && service_ != nullptr) service_->deviceGate_.leave();
}

QString VisionService::screenshotsDirectory() {
    // 与 apphelpers.cpp 的 yiukConfigRoot() 用同一套解析顺序：LOCALAPPDATA -> APPDATA ->
    // Qt 的 AppLocalDataLocation。vision 库属于低层，不能反过来依赖 UI 层，
    // 但必须落到同一个客户端数据目录，否则会出现互不相干的两份截图。
    QString root = qEnvironmentVariable("LOCALAPPDATA");
    if (root.isEmpty()) root = qEnvironmentVariable("APPDATA");
    if (root.isEmpty()) root = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    return QDir(root).filePath(QStringLiteral("Yiuk/DeviceAssist/screenshots"));
}

bool VisionService::capture(const QString &sessionId, const CaptureOptions &options,
                            const std::atomic_bool *cancelled, CaptureOutcome *outcome, QString *code,
                            QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (sessionId.isEmpty()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("会话 ID 为空"));

    // 设备级门在最外层：两个会话同时抓屏时必须排队，不能一起抢 CPU 与 GDI。
    DeviceLock deviceLock(*this, cancelled, code, message);
    if (!deviceLock.held()) return false;

    if (!limiter_.acquire(sessionId, cancelled, message)) {
        if (code) *code = QStringLiteral("CANCELLED");
        return false;
    }
    LimiterGuard guard{&limiter_, sessionId};

    // 布局变了就让所有旧 id 失效：不能拿"上一块屏幕"的坐标去点新画面。
    const QString layout = currentLayoutVersion();
    if (layout != layoutVersion_) {
        store_.forgetAll();
        layoutVersion_ = layout;
    }

    if (cancelled != nullptr && cancelled->load())
        return fail(QStringLiteral("CANCELLED"), QStringLiteral("操作已被取消"));

    ScreenCapture shot;
    QString error;
    if (!captureDesktop(&shot, &error)) return fail(QStringLiteral("SCREENSHOT_FAILED"), error);

    const int maxWidth = qBound(0, options.maxWidth, kMaxOutputWidth);
    const QImage image = scaleToWidth(shot.image, maxWidth);
    if (image.isNull()) return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("缩放后图像为空"));

    shot.geometry.width = image.width();
    shot.geometry.height = image.height();

    const QString id = SnapshotStore::newId();
    SnapshotRef ref;
    ref.id = id;
    ref.geometry = shot.geometry;
    ref.layoutVersion = layout;
    ref.takenAtMs = QDateTime::currentMSecsSinceEpoch();

    if (options.saveToDisk) {
        const QString format = normalizedFormat(options.format);
        const QString fileName = QStringLiteral("%1-%2-%3.%4")
                                     .arg(sessionId.left(8))
                                     .arg(ref.takenAtMs)
                                     .arg(id)
                                     .arg(format);
        const QString path = QDir(screenshotsDirectory()).filePath(fileName);
        if (!saveImage(image, path, format, options.jpegQuality, &error))
            return fail(QStringLiteral("SCREENSHOT_SAVE_FAILED"), error);
        ref.filePath = path;
    }

    if (!store_.remember(sessionId, ref))
        return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("无法登记快照"));

    // 崩溃/断电会留下不在任何快照里的文件：每次抓屏顺手清掉 5 分钟前的残留。
    pruneOrphanScreenshots(screenshotsDirectory(), 5 * 60 * 1000, store_.trackedFiles(),
                           QDateTime::currentMSecsSinceEpoch());

    if (outcome) {
        outcome->screenshotId = ref.id;
        outcome->filePath = ref.filePath;
        outcome->sizeBytes = ref.filePath.isEmpty() ? 0 : QFileInfo(ref.filePath).size();
        outcome->geometry = ref.geometry;
        outcome->layoutVersion = ref.layoutVersion;
    }
    return true;
}

bool VisionService::validateScreenshotId(const QString &sessionId, const QString &id, QString *code,
                                         QString *message) const {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    SnapshotRef latest;
    if (!store_.latest(sessionId, &latest))
        return fail(QStringLiteral("SCREENSHOT_REQUIRED"),
                    QStringLiteral("该会话还没有截图；坐标必须以最近一次截图为基准"));
    if (id.isEmpty()) return true;
    if (id == latest.id) return true;

    SnapshotRef ref;
    switch (store_.validate(sessionId, id, &ref)) {
        case SnapshotStore::Validation::Ok:
            return true;
        case SnapshotStore::Validation::Stale:
            return fail(QStringLiteral("STALE_SCREENSHOT"),
                        QStringLiteral("screenshot_id 已过期（有更新的截图或桌面布局变化），请重新截图后再操作"));
        case SnapshotStore::Validation::Unknown:
            break;
    }
    return fail(QStringLiteral("STALE_SCREENSHOT"),
                QStringLiteral("screenshot_id 不是本会话的截图，请重新截图后再操作"));
}

bool VisionService::latestScreenshot(const QString &sessionId, SnapshotRef *out) const {
    return store_.latest(sessionId, out);
}

namespace {

// 可取消的等待：点击后的 settle 期间取消也要立刻生效。
bool waitCancellable(int milliseconds, const std::atomic_bool *cancelled) {
    int remaining = milliseconds;
    while (remaining > 0) {
        if (cancelled != nullptr && cancelled->load()) return false;
        const int slice = qMin(remaining, 10);
        QThread::msleep(static_cast<unsigned long>(slice));
        remaining -= slice;
    }
    return cancelled == nullptr || !cancelled->load();
}

}  // namespace

bool VisionService::clickWithOptions(const QString &sessionId, const QString &screenshotId, int x, int y,
                                     const QString &button, bool doubleClick, bool detectChange, int settleMs,
                                     const std::atomic_bool *cancelled, QJsonObject *data, QString *code,
                                     QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (data == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("data 为空"));

    DeviceLock lock(*this, cancelled, code, message);
    if (!lock.held()) return false;

    int screenX = 0;
    int screenY = 0;
    if (!toScreenPoint(sessionId, screenshotId, x, y, &screenX, &screenY, code, message)) return false;

    // 前台标题必须在点击之前取：点击本身可能切换前台窗口。
    const QString foreground = yiuk::input::foregroundTitle();
    QVector<uchar> before;
    if (detectChange && !yiuk::input::sampleRegion(screenX, screenY, &before, message))
        return fail(QStringLiteral("INPUT_FAILED"), *message);

    QString error;
    if (!yiuk::input::click(screenX, screenY, button, doubleClick ? 2 : 1, cancelled, &error)) {
        const bool cancelledByCaller = cancelled != nullptr && cancelled->load();
        return fail(cancelledByCaller ? QStringLiteral("CANCELLED") : QStringLiteral("INPUT_FAILED"), error);
    }

    QJsonObject payload{
        {QStringLiteral("x"), x},
        {QStringLiteral("y"), y},
        {QStringLiteral("screen_x"), screenX},
        {QStringLiteral("screen_y"), screenY},
        {QStringLiteral("button"), button},
        {QStringLiteral("double"), doubleClick},
        {QStringLiteral("foreground_title"), foreground},
    };
    if (detectChange) {
        if (!waitCancellable(settleMs, cancelled))
            return fail(QStringLiteral("CANCELLED"), QStringLiteral("操作已被取消"));
        QVector<uchar> after;
        if (!yiuk::input::sampleRegion(screenX, screenY, &after, message))
            return fail(QStringLiteral("INPUT_FAILED"), *message);
        const yiuk::input::ChangeMetrics metrics = yiuk::input::compareSamples(before, after);
        payload.insert(QStringLiteral("diff_ratio"), metrics.diffRatio);
        payload.insert(QStringLiteral("changed_ratio"), metrics.changedRatio);
    }
    *data = payload;
    return true;
}

bool VisionService::searchText(const QString &sessionId, const TextQuery &query,
                               const std::atomic_bool *cancelled, QJsonObject *data, QString *code,
                               QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (data == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("data 为空"));

    // 坐标基准：没有就必须先抓一张（不落盘），否则返回的坐标无处可用。
    SnapshotRef ref;
    if (!latestScreenshot(sessionId, &ref)) {
        CaptureOptions options;
        options.saveToDisk = false;
        CaptureOutcome outcome;
        if (!capture(sessionId, options, cancelled, &outcome, code, message)) return false;
        if (!latestScreenshot(sessionId, &ref))
            return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("无法建立坐标基准"));
    }

    TextSearchOutcome outcome;
    if (!findText(query, &outcome, code, message)) return false;

    QJsonArray matches;
    for (const TextMatch &hit : outcome.matches) {
        QRect shotRect;
        SnapshotRef ignored;
        if (!screenRectToScreenshot(sessionId, hit.rect, &shotRect, &ignored, code, message)) continue;
        const int left = shotRect.left();
        const int top = shotRect.top();
        // bbox 为 [left,top,right,bottom]，右下开区间；center 取像素中心。
        matches.append(QJsonObject{
            {QStringLiteral("bbox"), QJsonArray{left, top, left + shotRect.width(), top + shotRect.height()}},
            {QStringLiteral("center"),
             QJsonArray{left + shotRect.width() / 2, top + shotRect.height() / 2}},
            {QStringLiteral("score"), hit.score},
            {QStringLiteral("source"), hit.source},
            {QStringLiteral("text"), hit.name},
            {QStringLiteral("control_type"), hit.controlType},
        });
    }

    QJsonObject payload = geometryToJson(ref.geometry);
    payload.insert(QStringLiteral("captured"), true);
    payload.insert(QStringLiteral("screenshot_id"), ref.id);
    payload.insert(QStringLiteral("layout_version"), ref.layoutVersion);
    payload.insert(QStringLiteral("matches"), matches);
    payload.insert(QStringLiteral("truncated"), outcome.truncated);
    payload.insert(QStringLiteral("visited_nodes"), outcome.visitedNodes);
    payload.insert(QStringLiteral("elapsed_ms"), outcome.elapsedMs);
    payload.insert(QStringLiteral("source"), QStringLiteral("uia"));
    *data = payload;
    return true;
}

bool VisionService::screenRectToScreenshot(const QString &sessionId, const QRect &screenRect, QRect *out,
                                            SnapshotRef *ref, QString *code, QString *message) const {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    SnapshotRef latest;
    if (!store_.latest(sessionId, &latest))
        return fail(QStringLiteral("SCREENSHOT_REQUIRED"),
                    QStringLiteral("该会话还没有截图；坐标必须以最近一次截图为基准"));
    const ScreenGeometry &geometry = latest.geometry;
    if (geometry.width <= 0 || geometry.height <= 0 || geometry.screenWidth <= 0 || geometry.screenHeight <= 0)
        return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("快照缺少有效的几何信息"));
    if (ref) *ref = latest;
    if (out) {
        const auto scaleX = [&](int value) {
            return qRound(static_cast<double>(value - geometry.left) * geometry.width / geometry.screenWidth);
        };
        const auto scaleY = [&](int value) {
            return qRound(static_cast<double>(value - geometry.top) * geometry.height / geometry.screenHeight);
        };
        const int left = scaleX(screenRect.left());
        const int top = scaleY(screenRect.top());
        const int right = scaleX(screenRect.left() + screenRect.width());
        const int bottom = scaleY(screenRect.top() + screenRect.height());
        *out = QRect(left, top, qMax(1, right - left), qMax(1, bottom - top));
    }
    return true;
}

bool VisionService::releaseScreenshotFile(const QString &sessionId, const QString &path, QString *code,
                                          QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (path.isEmpty()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("path 为空"));
    // 只允许删截图目录里的文件：这条能力不能被用来删用户的任意文件。
    const QString canonical = QFileInfo(path).canonicalFilePath();
    const QString allowedRoot = QFileInfo(screenshotsDirectory()).canonicalFilePath();
    if (canonical.isEmpty() || allowedRoot.isEmpty() || !canonical.startsWith(allowedRoot + QLatin1Char('/')))
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("path 不在客户端截图目录内，拒绝删除"));

    // 元数据保留：screenshot_id 依然可以作为坐标基准，只是没有图片了。
    store_.forgetFile(sessionId, path);
    QFile::remove(canonical);
    return true;
}

int VisionService::pruneOrphanScreenshots(const QString &directory, qint64 olderThanMs, const QStringList &keep,
                                          qint64 nowMs) {
    QDir dir(directory);
    if (!dir.exists()) return 0;
    const QFileInfoList entries = dir.entryInfoList(QDir::Files | QDir::NoSymLinks);
    int removed = 0;
    for (const QFileInfo &entry : entries) {
        const QString canonical = entry.canonicalFilePath();
        if (canonical.isEmpty() || keep.contains(canonical) || keep.contains(entry.absoluteFilePath())) continue;
        if (nowMs - entry.lastModified().toMSecsSinceEpoch() < olderThanMs) continue;
        if (QFile::remove(canonical)) ++removed;
    }
    return removed;
}

bool VisionService::toScreenPoint(const QString &sessionId, const QString &screenshotId, int x, int y,
                                  int *screenX, int *screenY, QString *code, QString *message) const {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (!validateScreenshotId(sessionId, screenshotId, code, message)) return false;
    SnapshotRef ref;
    if (!store_.latest(sessionId, &ref))
        return fail(QStringLiteral("SCREENSHOT_REQUIRED"),
                    QStringLiteral("该会话还没有截图；坐标必须以最近一次截图为基准"));
    const ScreenGeometry &geometry = ref.geometry;
    if (geometry.width <= 0 || geometry.height <= 0)
        return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("快照缺少有效的输出尺寸"));
    if (x < 0 || y < 0 || x >= geometry.width || y >= geometry.height)
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("x/y 必须落在最近一次截图内（%1x%2）").arg(geometry.width).arg(geometry.height));
    // 截图可能是缩放后的（max_width），屏幕坐标必须按输出尺寸等比换回物理像素。
    if (screenX)
        *screenX = geometry.left + qRound(static_cast<double>(x) * geometry.screenWidth / geometry.width);
    if (screenY)
        *screenY = geometry.top + qRound(static_cast<double>(y) * geometry.screenHeight / geometry.height);
    return true;
}

void VisionService::forgetSession(const QString &sessionId) {
    store_.forgetSession(sessionId);
}

void VisionService::forgetAll() {
    store_.forgetAll();
}

}  // namespace yiuk::vision
