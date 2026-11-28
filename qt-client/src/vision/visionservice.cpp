#include "visionservice.h"

#include "ocrlocator.h"

#include "apppaths.h"

#include "input/inputcontroller.h"

#include <QBuffer>
#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QImage>
#include <QThread>
#include <QJsonArray>
#include <QJsonObject>
#include <QtMath>
#include <QSaveFile>
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

const double kAmbiguityScoreGap = 0.03;

namespace {

// 两个候选是不是「同一个视觉目标」。
//
// UIA 经常把按钮和它内部的文本节点一起返回（同一个字的两个框），OpenCV 的 NMS
// 也可能留下几乎重叠的两个框。它们不是歧义，否则一个按钮永远点不成。
// 判据：中心重合，或者一个框有 ≥ 60% 的面积被另一个盖住。
bool sameVisualTarget(const TargetCandidate &a, const TargetCandidate &b) {
    if (a.center() == b.center()) return true;
    const QRect overlap = a.bbox.intersected(b.bbox);
    if (overlap.isEmpty()) return false;
    const qint64 overlapArea = qint64(overlap.width()) * overlap.height();
    const qint64 smallerArea = qMin(qint64(a.bbox.width()) * a.bbox.height(),
                                    qint64(b.bbox.width()) * b.bbox.height());
    return smallerArea > 0 && overlapArea * 10 >= smallerArea * 6;
}

}  // namespace

TargetPickOutcome pickTarget(const QVector<TargetCandidate> &candidates, const QString &pickRaw) {
    TargetPickOutcome outcome;
    const QString pick = pickRaw.trimmed().toLower();
    if (candidates.isEmpty()) {
        outcome.status = TargetPickStatus::NotFound;
        outcome.message = QStringLiteral("没有命中任何候选");
        return outcome;
    }

    // 得分最高者；同分时取返回顺序靠前的那个（UIA 阅读顺序更接近人的预期）。
    const auto bestIndex = [&candidates]() {
        int best = 0;
        for (int i = 1; i < candidates.size(); ++i) {
            if (candidates.at(i).score > candidates.at(best).score) best = i;
        }
        return best;
    };

    if (pick.isEmpty() || pick == QLatin1String("unique")) {
        const int top = bestIndex();
        // 只把「不在同一个视觉目标上」的候选当竞争对手：同框的父子节点 / NMS 残留
        // 不是歧义，否则一个按钮永远点不成。
        int competitor = -1;
        for (int i = 0; i < candidates.size(); ++i) {
            if (i == top || sameVisualTarget(candidates.at(i), candidates.at(top))) continue;
            if (competitor < 0 || candidates.at(i).score > candidates.at(competitor).score) competitor = i;
        }
        outcome.index = top;
        outcome.competitor = competitor;
        if (competitor >= 0 && candidates.at(top).score - candidates.at(competitor).score < kAmbiguityScoreGap) {
            outcome.status = TargetPickStatus::Ambiguous;
            outcome.message = QStringLiteral("命中 %1 个候选，第一与第二名得分差小于 %2，不点击")
                                  .arg(candidates.size())
                                  .arg(kAmbiguityScoreGap, 0, 'g', 2);
            return outcome;
        }
        outcome.status = TargetPickStatus::Picked;
        return outcome;
    }

    if (pick == QLatin1String("best")) {
        outcome.status = TargetPickStatus::Picked;
        outcome.index = bestIndex();
        return outcome;
    }

    if (pick == QLatin1String("first")) {
        outcome.status = TargetPickStatus::Picked;
        outcome.index = 0;
        return outcome;
    }

    if (pick.startsWith(QLatin1String("index:"))) {
        bool ok = false;
        const int number = pick.mid(6).trimmed().toInt(&ok);
        if (!ok || number < 1 || number > candidates.size()) {
            outcome.status = TargetPickStatus::InvalidPick;
            outcome.message = QStringLiteral("pick=index:%1 超出候选范围（共 %2 个，从 1 起）")
                                  .arg(pick.mid(6).trimmed(), QString::number(candidates.size()));
            return outcome;
        }
        outcome.status = TargetPickStatus::Picked;
        outcome.index = number - 1;
        return outcome;
    }

    outcome.status = TargetPickStatus::InvalidPick;
    outcome.message = QStringLiteral("pick 只能是 unique / best / first / index:N，收到 %1").arg(pickRaw);
    return outcome;
}

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
    release();
}

void VisionService::DeviceLock::release() {
    if (held_ && service_ != nullptr) {
        service_->deviceGate_.leave();
        held_ = false;
    }
}

QString VisionService::screenshotsDirectory() {
    // 跟配置同一个根：程序目录 data\screenshots（绿色版），不可写时才回退用户目录。
    return programDataPath(QStringLiteral("screenshots"));
}

bool VisionService::capture(const QString &sessionId, const CaptureOptions &options,
                            const std::atomic_bool *cancelled, CaptureOutcome *outcome, QString *code,
                            QString *message) {
    if (sessionId.isEmpty()) {
        if (code) *code = QStringLiteral("INVALID_ARGUMENT");
        if (message) *message = QStringLiteral("会话 ID 为空");
        return false;
    }
    // 设备级门在最外层：两个会话同时抓屏时必须排队，不能一起抢 CPU 与 GDI。
    DeviceLock deviceLock(*this, cancelled, code, message);
    if (!deviceLock.held()) return false;
    return captureLocked(sessionId, options, cancelled, outcome, code, message);
}

bool VisionService::captureLocked(const QString &sessionId, const CaptureOptions &options,
                                  const std::atomic_bool *cancelled, CaptureOutcome *outcome, QString *code,
                                  QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (sessionId.isEmpty()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("会话 ID 为空"));

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

    // 像素只留最近一张（约 16 MB 预算），save_template 靠它裁剪；旧快照的图会被放掉。
    ref.image = image;
    if (!store_.remember(sessionId, ref))
        return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("无法登记快照"));

    // 崩溃/断电会留下不在任何快照里的文件：每次抓屏顺手清掉 5 分钟前的残留。
    pruneOrphanScreenshots(screenshotsDirectory(), 5 * 60 * 1000, store_.trackedFiles(),
                           QDateTime::currentMSecsSinceEpoch());

    if (outcome && options.keepImage) outcome->image = image;
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
    if (data == nullptr) {
        if (code) *code = QStringLiteral("INVALID_ARGUMENT");
        if (message) *message = QStringLiteral("data 为空");
        return false;
    }
    DeviceLock lock(*this, cancelled, code, message);
    if (!lock.held()) return false;
    return clickPointLocked(sessionId, screenshotId, x, y, button, doubleClick, detectChange, settleMs, cancelled,
                            data, code, message);
}

bool VisionService::clickPointLocked(const QString &sessionId, const QString &screenshotId, int x, int y,
                                     const QString &button, bool doubleClick, bool detectChange, int settleMs,
                                     const std::atomic_bool *cancelled, QJsonObject *data, QString *code,
                                     QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (data == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("data 为空"));

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

namespace {

QJsonArray candidatesToJson(const QVector<TargetCandidate> &candidates) {
    QJsonArray array;
    for (const TargetCandidate &candidate : candidates) {
        const QRect &box = candidate.bbox;
        array.append(QJsonObject{
            {QStringLiteral("bbox"),
             QJsonArray{box.left(), box.top(), box.left() + box.width(), box.top() + box.height()}},
            {QStringLiteral("center"), QJsonArray{candidate.center().x(), candidate.center().y()}},
            {QStringLiteral("score"), candidate.score},
            {QStringLiteral("source"), candidate.source},
            {QStringLiteral("text"), candidate.text},
            {QStringLiteral("control_type"), candidate.controlType},
        });
    }
    return array;
}

}  // namespace

bool VisionService::clickTarget(const QString &sessionId, const ClickTargetOptions &options,
                                const std::atomic_bool *cancelled, QJsonObject *data, QString *code,
                                QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (data == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("data 为空"));
    if (sessionId.isEmpty()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("会话 ID 为空"));
    if (!options.useImage && options.text.text.trimmed().isEmpty())
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("text 不能为空"));
    QImage needle = options.needle;
    if (options.useImage && needle.isNull()) {
        if (options.templateId.isEmpty())
            return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("模板图像为空"));
        // 引用已保存的模板：像素读在门外面，文件 IO 不该占着设备门。
        if (!loadTemplate(options.templateId, &needle, code, message)) return false;
    }

    // pick 先校验：参数写错不应该先付一次抓屏的代价。
    const QString pick = options.pick.trimmed().isEmpty() ? QStringLiteral("unique") : options.pick.trimmed();
    const QString pickLower = pick.toLower();
    if (pickLower.startsWith(QLatin1String("index:"))) {
        bool ok = false;
        const int number = pickLower.mid(6).trimmed().toInt(&ok);
        if (!ok || number < 1)
            return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("pick 的 index:N 需要 N >= 1"));
    } else if (pickLower != QLatin1String("unique") && pickLower != QLatin1String("best") &&
               pickLower != QLatin1String("first")) {
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("pick 只能是 unique / best / first / index:N"));
    }

    // 等待的时候一轮一轮重新取门：不能为了等一个按钮把整台设备锁住 20 秒。
    const qint64 startedAtMs = QDateTime::currentMSecsSinceEpoch();
    const qint64 deadlineMs =
        options.waitUntilFound ? startedAtMs + qMax<qint64>(0, options.waitTimeoutMs) : startedAtMs;
    int attempts = 0;

    for (;;) {
    ++attempts;

    // 本轮全程持门：抓屏 -> 定位 -> 点击之间不能让别的会话插进来改画面或抢鼠标。
    DeviceLock lock(*this, cancelled, code, message);
    if (!lock.held()) return false;

    // 1. 抓一帧作为坐标基准。不落盘、不缩放：点击坐标必须与像素一一对应。
    CaptureOptions captureOptions;
    captureOptions.saveToDisk = false;
    captureOptions.keepImage = true;
    CaptureOutcome shot;
    if (!captureLocked(sessionId, captureOptions, cancelled, &shot, code, message)) return false;

    // 2. 定位。unique 至少要两个候选才判断得了歧义，所以把 limit 抬到 2。
    int limit = options.useImage ? options.image.limit : options.text.limit;
    if (pickLower == QLatin1String("unique")) limit = qMax(2, limit);
    limit = qBound(1, limit, 20);

    QVector<TargetCandidate> candidates;
    const QString wantedSource = options.useImage ? QString() : options.text.source.trimmed().toLower();
    if (!options.useImage && wantedSource != QLatin1String("auto") && wantedSource != QLatin1String("uia") &&
        wantedSource != QLatin1String("ocr"))
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("source 只能是 auto / uia / ocr"));
    QString source = options.useImage ? QStringLiteral("opencv") : QStringLiteral("uia");
    bool truncated = false;
    int visitedNodes = 0;
    int locateMs = 0;
    QString ocrLanguage;
    QString ocrNote;
    // 注意：click_target **不用** ROI 缓存。unique/best 必须验证全域有没有竞争候选，
    // 只看旧位置会漏掉歧义、点错目标——慢一点没关系，点错不行。
    TemplateStats stats;  // 图像分支的现场（零命中也要带回去给 hint 用）
    if (options.useImage) {
        TemplateOptions imageOptions = options.image;
        imageOptions.limit = limit;
        QVector<TemplateHit> hits;
        if (!matchTemplateImage(shot.image, needle, imageOptions, &hits, &stats, code, message)) return false;
        for (const TemplateHit &hit : hits) {
            TargetCandidate candidate;
            candidate.bbox = hit.rect;
            candidate.score = hit.score;
            candidate.source = source;
            candidates.append(candidate);
        }
    } else {
        TextQuery query = options.text;
        query.limit = limit;

        // UIA 优先（能拿到控件类型，也最准）；auto 模式下它失败或没命中都不算致命。
        if (wantedSource != QLatin1String("ocr")) {
            TextSearchOutcome outcome;
            if (!findText(query, &outcome, code, message)) {
                if (wantedSource == QLatin1String("uia")) return false;
                ocrNote = *message;  // 记下 UIA 为什么没给结果
            } else {
                truncated = outcome.truncated;
                visitedNodes = outcome.visitedNodes;
                locateMs = outcome.elapsedMs;
                for (const TextMatch &hit : outcome.matches) {
                    QRect rect;
                    SnapshotRef ignored;
                    // UIA 给的是物理屏幕坐标，必须换算到刚抓这一帧的坐标系。
                    if (!screenRectToScreenshot(sessionId, hit.rect, &rect, &ignored, code, message)) continue;
                    TargetCandidate candidate;
                    candidate.bbox = rect;
                    candidate.score = hit.score;
                    candidate.text = hit.name;
                    candidate.source = hit.source;
                    candidate.controlType = hit.controlType;
                    candidates.append(candidate);
                }
            }
        }

        // OCR 兜底：UIA 没有候选（或调用方直接要 ocr）时，在**同一帧**上识别。
        // 同一帧很关键：定位与点击共用这一帧，OCR 的框不需要再换一次坐标基准。
        if (candidates.isEmpty() && wantedSource != QLatin1String("uia")) {
            OcrOutcome ocr;
            QString ocrCode;
            QString ocrMessage;
            if (!recognizeText(shot.image, query.text, cancelled, &ocr, &ocrCode, &ocrMessage)) {
                if (ocrCode == QLatin1String("CANCELLED")) return fail(ocrCode, ocrMessage);
                if (wantedSource == QLatin1String("ocr"))
                    return fail(ocrCode, ocrMessage);
                ocrNote = ocrMessage;
            } else {
                ocrLanguage = ocr.language;
                locateMs += ocr.elapsedMs;
                QVector<TextMatch> ocrHits;
                matchOcrText(ocr, query, QRect(), &ocrHits);
                for (const TextMatch &hit : ocrHits) {
                    // OCR 的框直接就是这一帧的像素坐标（截图坐标）。
                    if (query.region.isValid() && !query.region.isEmpty()) {
                        const QRect screenRect = screenshotRectToScreen(shot.geometry, hit.rect);
                        if (!screenRect.isNull() && !screenRect.intersects(query.region)) continue;
                    }
                    TargetCandidate candidate;
                    candidate.bbox = hit.rect;
                    candidate.score = hit.score;
                    candidate.text = hit.name;
                    candidate.source = QStringLiteral("ocr");
                    candidates.append(candidate);
                }
                if (!candidates.isEmpty()) source = QStringLiteral("ocr");
            }
        }
    }

    // 3. 选择。未命中 / 歧义都在这里收口，保证不发任何 down 事件。
    QJsonObject detail = geometryToJson(shot.geometry);
    detail.insert(QStringLiteral("captured"), true);
    detail.insert(QStringLiteral("screenshot_id"), shot.screenshotId);
    detail.insert(QStringLiteral("layout_version"), shot.layoutVersion);
    detail.insert(QStringLiteral("matches"), candidatesToJson(candidates));
    detail.insert(QStringLiteral("candidate_count"), candidates.size());
    detail.insert(QStringLiteral("source"), source);
    detail.insert(QStringLiteral("pick"), pick);
    detail.insert(QStringLiteral("truncated"), truncated);
    detail.insert(QStringLiteral("visited_nodes"), visitedNodes);
    detail.insert(QStringLiteral("locate_ms"), locateMs);
    if (options.useImage) {
        detail.insert(QStringLiteral("threshold"), options.image.threshold);
        detail.insert(QStringLiteral("max_score"), stats.bestScore);
        detail.insert(QStringLiteral("uniform_area"), stats.uniformArea);
        detail.insert(QStringLiteral("area_stddev"), stats.areaStdDev);
    }
    if (!ocrLanguage.isEmpty()) detail.insert(QStringLiteral("ocr_language"), ocrLanguage);
    if (!ocrNote.isEmpty()) detail.insert(QStringLiteral("ocr_note"), ocrNote);

    const TargetPickOutcome choice = pickTarget(candidates, pick);
    if (choice.status != TargetPickStatus::Picked) {
        // 失败也把现场带回去：Agent 要靠候选、得分、阈值决定下一步怎么改参数。
        if (choice.index >= 0 && choice.index < candidates.size())
            detail.insert(QStringLiteral("top_score"), candidates.at(choice.index).score);
        if (choice.competitor >= 0 && choice.competitor < candidates.size())
            detail.insert(QStringLiteral("competitor_score"), candidates.at(choice.competitor).score);
        *data = detail;
        switch (choice.status) {
            case TargetPickStatus::NotFound: {
                // 等目标出现：没到截止时间就放门、等一轮再试；到了就带着现场报 NOT_FOUND。
                const bool expired = QDateTime::currentMSecsSinceEpoch() >= deadlineMs;
                if (options.waitUntilFound && !expired) {
                    lock.release();  // 轮间不霸占设备：另一个会话要截屏/点鼠标得能进来
                    if (!waitCancellable(500, cancelled))
                        return fail(QStringLiteral("CANCELLED"), QStringLiteral("操作已被取消"));
                    continue;
                }
                detail.insert(QStringLiteral("timed_out"), options.waitUntilFound && expired);
                detail.insert(QStringLiteral("attempts"), attempts);
                detail.insert(QStringLiteral("waited_ms"),
                              QDateTime::currentMSecsSinceEpoch() - startedAtMs);
                *data = detail;
                const QString why = options.waitUntilFound && expired
                                        ? QStringLiteral("等待 %1 ms 仍未命中任何候选（共 %2 轮）")
                                              .arg(options.waitTimeoutMs)
                                              .arg(attempts)
                                        : choice.message;
                return fail(QStringLiteral("NOT_FOUND"), why);
            }
            case TargetPickStatus::Ambiguous:
                return fail(QStringLiteral("AMBIGUOUS_TARGET"), choice.message);
            default:
                return fail(QStringLiteral("INVALID_ARGUMENT"), choice.message);
        }
    }

    // 4. 点击：落点取命中框中心（截图坐标），与 find_on_screen 的 center 同一算法。
    const TargetCandidate &chosen = candidates.at(choice.index);
    const QPoint point = chosen.center();
    const int settleMs = options.detectChange ? (options.settleMs > 0 ? options.settleMs : 250) : 0;
    QJsonObject clicked;
    if (!clickPointLocked(sessionId, shot.screenshotId, point.x(), point.y(),
                          options.button.isEmpty() ? QStringLiteral("left") : options.button, options.doubleClick,
                          options.detectChange, settleMs, cancelled, &clicked, code, message)) {
        return false;
    }

    QJsonObject payload = detail;
    for (auto it = clicked.begin(); it != clicked.end(); ++it) payload.insert(it.key(), it.value());
    const QRect &box = chosen.bbox;
    payload.insert(QStringLiteral("picked"),
                   QJsonObject{
                       {QStringLiteral("index"), choice.index + 1},
                       {QStringLiteral("bbox"),
                        QJsonArray{box.left(), box.top(), box.left() + box.width(), box.top() + box.height()}},
                       {QStringLiteral("center"), QJsonArray{point.x(), point.y()}},
                       {QStringLiteral("score"), chosen.score},
                       {QStringLiteral("source"), chosen.source},
                       {QStringLiteral("text"), chosen.text},
                       {QStringLiteral("control_type"), chosen.controlType},
                   });
    payload.insert(QStringLiteral("detect_change"), options.detectChange);
    payload.insert(QStringLiteral("attempts"), attempts);
    payload.insert(QStringLiteral("waited_ms"), QDateTime::currentMSecsSinceEpoch() - startedAtMs);
    payload.insert(QStringLiteral("timed_out"), false);
    payload.insert(QStringLiteral("elapsed_ms"), QDateTime::currentMSecsSinceEpoch() - startedAtMs);
    *data = payload;
    return true;
    }  // for (;;)
}

bool VisionService::matchImage(const QString &sessionId, const QImage &needle, const TemplateOptions &options,
                               const std::atomic_bool *cancelled, QJsonObject *data, QString *code,
                               QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (data == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("data 为空"));
    if (needle.isNull()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("模板图像为空"));

    CaptureOptions captureOptions;
    captureOptions.saveToDisk = false;
    captureOptions.keepImage = true;
    CaptureOutcome shot;
    if (!capture(sessionId, captureOptions, cancelled, &shot, code, message)) return false;
    if (shot.image.isNull()) return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("抓屏结果为空"));

    // ROI 缓存：只在这个调用只想要一个命中时启用（limit == 1）。
    // 要多个候选却只查旧位置，会悄悄少报结果——那比慢更糟。
    const QString roiKey = RoiCache::imageKey(needle, options, shot.layoutVersion);
    const qint64 nowMs = QDateTime::currentMSecsSinceEpoch();
    const QRect searchArea = options.region.isNull()
                                 ? QRect(0, 0, shot.image.width(), shot.image.height())
                                 : options.region.intersected(QRect(0, 0, shot.image.width(), shot.image.height()));
    bool roiCached = false;
    QRect searchedArea = searchArea;

    QVector<TemplateHit> hits;
    TemplateStats stats;
    if (qBound(1, options.limit, 20) == 1) {
        const QRect cached = roiCache_.lookup(roiKey, nowMs);
        const QRect roi = RoiCache::expand(cached, RoiCache::kMarginPx, searchArea);
        if (!roi.isNull() && roi.width() >= needle.width() && roi.height() >= needle.height()) {
            TemplateOptions narrowed = options;
            narrowed.region = roi;
            QVector<TemplateHit> roiHits;
            if (matchTemplateImage(shot.image, needle, narrowed, &roiHits, &stats, code, message) &&
                !roiHits.isEmpty()) {
                hits = roiHits;
                roiCached = true;
                searchedArea = roi;
            }
        }
    }
    if (hits.isEmpty()) {
        TemplateOptions full = options;
        full.region = options.region.isNull() ? QRect() : options.region;
        if (!matchTemplateImage(shot.image, needle, full, &hits, &stats, code, message)) return false;
        searchedArea = searchArea;
        // 记住这次的位置，下一次可以先在它附近找。
        if (!hits.isEmpty()) roiCache_.store(roiKey, hits.first().rect, nowMs);
    }


    QJsonArray matches;
    for (const TemplateHit &hit : hits) {
        matches.append(QJsonObject{
            {QStringLiteral("bbox"),
             QJsonArray{hit.rect.left(), hit.rect.top(), hit.rect.left() + hit.rect.width(),
                        hit.rect.top() + hit.rect.height()}},
            {QStringLiteral("center"),
             QJsonArray{hit.rect.left() + hit.rect.width() / 2, hit.rect.top() + hit.rect.height() / 2}},
            {QStringLiteral("score"), hit.score},
            {QStringLiteral("source"), QStringLiteral("opencv")},
            {QStringLiteral("text"), QString()},
        });
    }

    QJsonObject payload = geometryToJson(shot.geometry);
    payload.insert(QStringLiteral("captured"), true);
    payload.insert(QStringLiteral("screenshot_id"), shot.screenshotId);
    payload.insert(QStringLiteral("layout_version"), shot.layoutVersion);
    payload.insert(QStringLiteral("matches"), matches);
    payload.insert(QStringLiteral("truncated"), false);
    payload.insert(QStringLiteral("visited_nodes"), 0);
    payload.insert(QStringLiteral("elapsed_ms"), 0);
    payload.insert(QStringLiteral("source"), QStringLiteral("opencv"));
    // 零命中也要把现场带回去：最高分与"区域是不是纯色"决定 hint 该说什么。
    payload.insert(QStringLiteral("threshold"), options.threshold);
    payload.insert(QStringLiteral("max_score"), stats.bestScore);
    payload.insert(QStringLiteral("uniform_area"), stats.uniformArea);
    payload.insert(QStringLiteral("area_stddev"), stats.areaStdDev);
    // ROI 缓存的现场：命中来自旧位置附近就标注出来，并给出实际搜索面积与整屏面积。
    payload.insert(QStringLiteral("roi_cached"), roiCached);
    payload.insert(QStringLiteral("searched_area"),
                   searchedArea.isNull() ? 0 : searchedArea.width() * searchedArea.height());
    payload.insert(QStringLiteral("search_area"),
                   searchArea.isNull() ? 0 : searchArea.width() * searchArea.height());
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

    // 定位来源：auto（UIA 优先，找不到再 OCR）/ uia / ocr。
    const QString wanted = query.source.trimmed().toLower();
    if (wanted != QLatin1String("auto") && wanted != QLatin1String("uia") && wanted != QLatin1String("ocr"))
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("source 只能是 auto / uia / ocr"));

    // 坐标基准：没有就必须先抓一张（不落盘），否则返回的坐标无处可用。
    // 明确只要 OCR 时不先抓：下面那次抓屏会同时建立基准，少一次全屏抓取。
    SnapshotRef ref;
    bool haveRef = latestScreenshot(sessionId, &ref);
    QVector<TextMatch> hits;  // 统一用**物理屏幕坐标**
    bool truncated = false;
    int visitedNodes = 0;
    int elapsedMs = 0;

    if (wanted != QLatin1String("ocr")) {
        if (!haveRef) {
            CaptureOptions options;
            options.saveToDisk = false;
            CaptureOutcome outcome;
            if (!capture(sessionId, options, cancelled, &outcome, code, message)) return false;
            haveRef = latestScreenshot(sessionId, &ref);
            if (!haveRef) return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("无法建立坐标基准"));
        }
        TextSearchOutcome outcome;
        if (!findText(query, &outcome, code, message)) {
            // auto：UIA 用不了（锁屏、前台不是普通窗口、没有前台窗口）不该挡住 OCR 兜底。
            if (wanted == QLatin1String("uia")) return false;
        } else {
            truncated = outcome.truncated;
            visitedNodes = outcome.visitedNodes;
            elapsedMs = outcome.elapsedMs;
            hits = outcome.matches;
        }
    }

    QString usedSource = hits.isEmpty() ? QStringLiteral("uia") : QStringLiteral("uia");
    QString ocrLanguage;
    QString ocrNote;
    int ocrMs = 0;

    if (hits.isEmpty() && wanted != QLatin1String("uia")) {
        // OCR 要像素：抓一帧不落盘的画面，并让它成为新的坐标基准。
        CaptureOptions captureOptions;
        captureOptions.saveToDisk = false;
        captureOptions.keepImage = true;
        CaptureOutcome shot;
        if (!capture(sessionId, captureOptions, cancelled, &shot, code, message)) return false;
        ref = SnapshotRef{};
        if (!latestScreenshot(sessionId, &ref))
            return fail(QStringLiteral("SCREENSHOT_FAILED"), QStringLiteral("无法建立坐标基准"));

        OcrOutcome ocr;
        QString ocrCode;
        QString ocrMessage;
        if (!recognizeText(shot.image, query.text, cancelled, &ocr, &ocrCode, &ocrMessage)) {
            if (ocrCode == QLatin1String("CANCELLED")) return fail(ocrCode, ocrMessage);
            if (wanted == QLatin1String("ocr")) {
                const QString hint = ocrCode == QLatin1String("OCR_UNAVAILABLE")
                                         ? QStringLiteral("在设备上安装 OCR 语言包（设置 → 时间和语言 → 语言），或用 image 模板匹配")
                                         : QString();
                if (code) *code = ocrCode;
                if (message) *message = ocrMessage;
                // 显式要 OCR 却没成：带 hint 交给上层，不当成"找不到"。
                if (data) {
                    QJsonObject detail = geometryToJson(ref.geometry);
                    detail.insert(QStringLiteral("captured"), true);
                    detail.insert(QStringLiteral("screenshot_id"), ref.id);
                    detail.insert(QStringLiteral("source"), QStringLiteral("ocr"));
                    detail.insert(QStringLiteral("ocr_hint"), hint);
                    *data = detail;
                }
                return false;
            }
            ocrNote = ocrMessage;  // auto：记下为什么没用 OCR，然后照常返回空结果
        } else {
            ocrLanguage = ocr.language;
            ocrMs = ocr.elapsedMs;
            QVector<TextMatch> ocrHits;
            matchOcrText(ocr, query, QRect(), &ocrHits);
            for (const TextMatch &hit : ocrHits) {
                TextMatch screenMatch = hit;
                screenMatch.rect = screenshotRectToScreen(shot.geometry, hit.rect);
                if (screenMatch.rect.isNull()) continue;
                // region 语义与 UIA 一致：物理屏幕坐标，相交即算。
                if (query.region.isValid() && !query.region.isEmpty() &&
                    !screenMatch.rect.intersects(query.region))
                    continue;
                hits.append(screenMatch);
            }
            if (!hits.isEmpty()) usedSource = QStringLiteral("ocr");
            elapsedMs += ocrMs;
        }
    }

    QJsonArray matches;
    int emitted = 0;
    const int limit = qBound(1, query.limit, 20);
    for (const TextMatch &hit : hits) {
        if (emitted >= limit) break;
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
        ++emitted;
    }

    QJsonObject payload = geometryToJson(ref.geometry);
    payload.insert(QStringLiteral("captured"), true);
    payload.insert(QStringLiteral("screenshot_id"), ref.id);
    payload.insert(QStringLiteral("layout_version"), ref.layoutVersion);
    payload.insert(QStringLiteral("matches"), matches);
    payload.insert(QStringLiteral("truncated"), truncated);
    payload.insert(QStringLiteral("visited_nodes"), visitedNodes);
    payload.insert(QStringLiteral("elapsed_ms"), elapsedMs);
    payload.insert(QStringLiteral("source"), usedSource);
    payload.insert(QStringLiteral("requested_source"), wanted.isEmpty() ? QStringLiteral("auto") : wanted);
    if (!ocrLanguage.isEmpty()) payload.insert(QStringLiteral("ocr_language"), ocrLanguage);
    if (!ocrNote.isEmpty()) payload.insert(QStringLiteral("ocr_note"), ocrNote);
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


namespace {

// 模板目录内的索引：只放元数据，用来做配额统计与尺寸/缩放查询。
QString templateIndexPath() {
    return QDir(VisionService::templatesDirectory()).filePath(QStringLiteral("index.json"));
}

bool validTemplateId(const QString &id) {
    if (id.size() != 12) return false;
    for (const QChar ch : id) {
        const ushort value = ch.unicode();
        const bool digit = value >= '0' && value <= '9';
        const bool lower = value >= 'a' && value <= 'f';
        if (!digit && !lower) return false;
    }
    return true;
}

QJsonObject readTemplateIndex() {
    QFile file(templateIndexPath());
    if (!file.open(QIODevice::ReadOnly)) return QJsonObject();
    const QJsonDocument document = QJsonDocument::fromJson(file.readAll());
    return document.isObject() ? document.object() : QJsonObject();
}

// 原子写：先写临时文件再改名，崩在中间也不会留下半个模板或半个 index。
bool writeAtomically(const QString &path, const QByteArray &bytes, QString *error) {
    QSaveFile file(path);
    if (!file.open(QIODevice::WriteOnly)) {
        if (error) *error = file.errorString();
        return false;
    }
    if (file.write(bytes) != bytes.size()) {
        if (error) *error = file.errorString();
        file.cancelWriting();
        return false;
    }
    if (!file.commit()) {
        if (error) *error = file.errorString();
        return false;
    }
    return true;
}

}  // namespace

QString VisionService::templatesDirectory() {
    // 与截图同一个根：程序目录 data\templates（绿色版），不可写时回退用户目录。
    return programDataPath(QStringLiteral("templates"));
}

bool VisionService::saveTemplate(const QString &sessionId, const QString &screenshotId, const QRect &region,
                                 TemplateOutcome *outcome, QString *code, QString *message) {
    const auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (outcome == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("outcome 为空"));
    *outcome = TemplateOutcome{};
    if (sessionId.isEmpty()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("会话 ID 为空"));
    if (region.width() <= 0 || region.height() <= 0)
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("region 必须有正面积"));

    SnapshotRef ref;
    if (!store_.latest(sessionId, &ref))
        return fail(QStringLiteral("SCREENSHOT_REQUIRED"),
                    QStringLiteral("该会话还没有截图；先 take_screenshot 再裁剪模板"));
    // 只认"最近一张"：旧截图的像素已经放掉，绝不能拿现在的画面顶替。
    if (!screenshotId.isEmpty() && screenshotId != ref.id)
        return fail(QStringLiteral("STALE_SCREENSHOT"),
                    QStringLiteral("screenshot_id 不是最近一张截图；模板只能从当前这一帧裁剪，请重新截图"));
    if (ref.image.isNull())
        return fail(QStringLiteral("STALE_SCREENSHOT"),
                    QStringLiteral("这一帧的像素已被回收（只在最近一张快照上保留）；请重新 take_screenshot"));
    if (ref.layoutVersion != currentLayoutVersion())
        return fail(QStringLiteral("STALE_SCREENSHOT"),
                    QStringLiteral("桌面布局已变化；请重新 take_screenshot 再裁剪模板"));

    const QRect imageRect(0, 0, ref.image.width(), ref.image.height());
    const QRect area = region.intersected(imageRect);
    if (area.width() <= 0 || area.height() <= 0 || area != region)
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("region 必须完整落在最近一张截图内（%1x%2）")
                        .arg(imageRect.width())
                        .arg(imageRect.height()));
    if (area.width() > 2048 || area.height() > 2048)
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("模板 %1x%2 超过 2048x2048 上限").arg(area.width()).arg(area.height()));

    const QImage crop = ref.image.copy(area);
    QByteArray png;
    {
        QBuffer buffer(&png);
        if (!buffer.open(QIODevice::WriteOnly) || !crop.save(&buffer, "PNG"))
            return fail(QStringLiteral("TEMPLATE_SAVE_FAILED"), QStringLiteral("模板编码失败"));
    }
    if (png.size() > kMaxTemplateBytes)
        return fail(QStringLiteral("TEMPLATE_TOO_LARGE"),
                    QStringLiteral("模板编码后 %1 字节，超过单个上限 %2 字节（缩小 region 或降低分辨率）")
                        .arg(png.size())
                        .arg(kMaxTemplateBytes));

    // 配额：数目与总量都要挡，且拒绝要明确（不静默删别人的模板）。
    QJsonObject index = readTemplateIndex();
    QJsonObject templates = index.value(QStringLiteral("templates")).toObject();
    qint64 totalBytes = 0;
    for (auto it = templates.begin(); it != templates.end(); ++it)
        totalBytes += static_cast<qint64>(it.value().toObject().value(QStringLiteral("bytes")).toDouble());
    if (templates.size() >= kMaxTemplateCount)
        return fail(QStringLiteral("TEMPLATE_QUOTA_EXCEEDED"),
                    QStringLiteral("模板数量已达上限 %1 个；请清理 %2 后重试")
                        .arg(kMaxTemplateCount)
                        .arg(templatesDirectory()));
    if (totalBytes + png.size() > kMaxTemplatesTotalBytes)
        return fail(QStringLiteral("TEMPLATE_QUOTA_EXCEEDED"),
                    QStringLiteral("模板总大小将超过 %1 字节（当前 %2）；请清理 %3 后重试")
                        .arg(kMaxTemplatesTotalBytes)
                        .arg(totalBytes)
                        .arg(templatesDirectory()));

    const QString directory = templatesDirectory();
    if (!QDir().mkpath(directory))
        return fail(QStringLiteral("TEMPLATE_SAVE_FAILED"),
                    QStringLiteral("无法创建模板目录 %1").arg(directory));
    const QString id = SnapshotStore::newId();
    QString writeError;
    if (!writeAtomically(QDir(directory).filePath(id + QStringLiteral(".png")), png, &writeError))
        return fail(QStringLiteral("TEMPLATE_SAVE_FAILED"),
                    QStringLiteral("模板写入失败：%1").arg(writeError));

    // 记录来源缩放：模板是在这张截图坐标系里裁的，之后按同一缩放匹配才有意义。
    const double scale = ref.geometry.screenWidth > 0
                             ? static_cast<double>(ref.geometry.width) / ref.geometry.screenWidth
                             : 1.0;
    templates.insert(id, QJsonObject{
                             {QStringLiteral("width"), area.width()},
                             {QStringLiteral("height"), area.height()},
                             {QStringLiteral("bytes"), static_cast<double>(png.size())},
                             {QStringLiteral("scale"), scale},
                             {QStringLiteral("created_ms"),
                              static_cast<double>(QDateTime::currentMSecsSinceEpoch())},
                         });
    index.insert(QStringLiteral("templates"), templates);
    if (!writeAtomically(templateIndexPath(), QJsonDocument(index).toJson(QJsonDocument::Compact),
                         &writeError)) {
        // 索引写不进去就把刚写的 PNG 删掉，别留一个没人登记的孤儿文件。
        QFile::remove(QDir(directory).filePath(id + QStringLiteral(".png")));
        return fail(QStringLiteral("TEMPLATE_SAVE_FAILED"),
                    QStringLiteral("模板索引写入失败：%1").arg(writeError));
    }

    outcome->templateId = id;
    outcome->width = area.width();
    outcome->height = area.height();
    outcome->sizeBytes = png.size();
    outcome->region = area;
    return true;
}

bool VisionService::loadTemplate(const QString &templateId, QImage *image, QString *code,
                                 QString *message) const {
    const auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (image == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("image 为空"));
    *image = QImage();
    // id 只允许 12 位小写 hex：这既是格式校验，也是路径穿越防线。
    if (!validTemplateId(templateId))
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("template_id 必须是 12 位小写十六进制"));
    const QString path = QDir(templatesDirectory()).filePath(templateId + QStringLiteral(".png"));
    const QFileInfo info(path);
    if (!info.exists())
        return fail(QStringLiteral("TEMPLATE_NOT_FOUND"),
                    QStringLiteral("没有这个模板（可能已被清理）：%1").arg(templateId));
    if (info.size() > kMaxTemplateBytes)
        return fail(QStringLiteral("TEMPLATE_CORRUPTED"),
                    QStringLiteral("模板 %1 体积异常（%2 字节）").arg(templateId).arg(info.size()));
    QImage loaded;
    if (!loaded.load(path))
        return fail(QStringLiteral("TEMPLATE_CORRUPTED"),
                    QStringLiteral("模板 %1 无法解码（文件可能已损坏）").arg(templateId));
    if (loaded.width() > 2048 || loaded.height() > 2048)
        return fail(QStringLiteral("TEMPLATE_CORRUPTED"),
                    QStringLiteral("模板 %1 尺寸异常（%2x%3）")
                        .arg(templateId)
                        .arg(loaded.width())
                        .arg(loaded.height()));
    *image = loaded;
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
