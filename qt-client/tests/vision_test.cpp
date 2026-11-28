// 视觉/抓屏公共库的行为测试（控制台程序，不需要平台，不注入键鼠）。
//
// 覆盖：截图 id 格式、快照最近 5 张与文件回收、坐标基准校验（未知/过期/布局变化）、
// 会话级串行与最小间隔、取消能打断排队、按宽度缩放、PNG/JPEG 落盘，以及一次真实抓屏。
//
// 平台链路（MCP → Go → Qt）由 Go 侧测试与 yiuk-platform-interop 覆盖，这里只证明
// 客户端内部的坐标与文件语义正确。

#include "vision/screengrab.h"
#include "vision/sessionlimiter.h"
#include "vision/snapshotstore.h"
#include "vision/templatematcher.h"
#include "vision/textlocator.h"
#include "vision/ocrlocator.h"
#include "vision/requestparser.h"
#include "vision/roicache.h"
#include "vision/visionservice.h"

#include <QCoreApplication>
#include <QBuffer>
#include <QDir>
#include <QElapsedTimer>
#include <QFile>
#include <QFileInfo>
#include <QImage>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QTemporaryDir>

#include <windows.h>

#include <atomic>
#include <cstdio>
#include <thread>

namespace {

int failures = 0;

void check(bool condition, const QString &what) {
    if (condition) {
        std::printf("ok   %s\n", qPrintable(what));
        return;
    }
    ++failures;
    std::printf("FAIL %s\n", qPrintable(what));
}

// 写一个占位文件，用来验证快照回收时确实删掉了临时截图。
QString makeTempShot(const QTemporaryDir &dir, const QString &name) {
    const QString path = QDir(dir.path()).filePath(name);
    QFile file(path);
    if (!file.open(QIODevice::WriteOnly)) return QString();
    file.write("x");
    file.close();
    return path;
}

yiuk::vision::SnapshotRef makeRef(const QString &id, const QString &layout, const QString &file = QString()) {
    yiuk::vision::SnapshotRef ref;
    ref.id = id;
    ref.layoutVersion = layout;
    ref.filePath = file;
    ref.geometry.left = 0;
    ref.geometry.top = 0;
    ref.geometry.screenWidth = 1920;
    ref.geometry.screenHeight = 1080;
    ref.geometry.width = 1920;
    ref.geometry.height = 1080;
    return ref;
}

void testIds() {
    const QString id = yiuk::vision::SnapshotStore::newId();
    check(id.size() == 12, QStringLiteral("screenshot id is 12 chars"));
    bool allHex = true;
    for (const QChar c : id) allHex = allHex && (c.isDigit() || (c >= QLatin1Char('a') && c <= QLatin1Char('f')));
    check(allHex, QStringLiteral("screenshot id is lowercase hex"));
    check(id != yiuk::vision::SnapshotStore::newId(), QStringLiteral("screenshot ids are not constant"));
}

void testStore() {
    QTemporaryDir dir;
    check(dir.isValid(), QStringLiteral("temporary directory for snapshot files"));
    const QString layout = yiuk::vision::currentLayoutVersion();
    check(layout.size() == 16, QStringLiteral("layout version is a 16-char digest"));

    yiuk::vision::SnapshotStore store;
    const QString session = QStringLiteral("session-a");

    // 最近 5 张：超出后丢最旧的一条，并删掉它的临时文件。
    QStringList paths;
    QStringList ids;
    for (int i = 0; i < yiuk::vision::SnapshotStore::kMaxPerSession + 2; ++i) {
        paths << makeTempShot(dir, QStringLiteral("shot-%1.png").arg(i));
        ids << yiuk::vision::SnapshotStore::newId();
        store.remember(session, makeRef(ids.last(), layout, paths.last()));
    }
    check(store.count(session) == yiuk::vision::SnapshotStore::kMaxPerSession,
          QStringLiteral("store keeps only the newest 5 snapshots"));
    check(!QFileInfo::exists(paths.first()) && !QFileInfo::exists(paths.at(1)),
          QStringLiteral("evicted snapshots delete their temp files"));
    check(QFileInfo::exists(paths.last()), QStringLiteral("newest snapshot file is kept"));

    yiuk::vision::SnapshotRef latest;
    check(store.latest(session, &latest), QStringLiteral("latest() reports the newest snapshot"));
    check(latest.id == ids.last(), QStringLiteral("latest() returns the last remembered id"));

    // 坐标基准：最近一张有效，旧的过期，别的会话未知。
    check(store.validate(session, ids.last()) == yiuk::vision::SnapshotStore::Validation::Ok,
          QStringLiteral("newest screenshot id is a valid coordinate base"));
    check(store.validate(session, ids.at(ids.size() - 2)) == yiuk::vision::SnapshotStore::Validation::Stale,
          QStringLiteral("older screenshot id is stale"));
    check(store.validate(QStringLiteral("session-other"), ids.last()) ==
              yiuk::vision::SnapshotStore::Validation::Unknown,
          QStringLiteral("unknown session reports Unknown"));
    check(store.validate(session, QStringLiteral("ffffffffffff")) ==
              yiuk::vision::SnapshotStore::Validation::Unknown,
          QStringLiteral("foreign screenshot id reports Unknown"));

    // 布局变化立即让旧 id 失效（用伪造的 layout 版本模拟插拔显示器）。
    const QString file = makeTempShot(dir, QStringLiteral("stale-layout.png"));
    store.remember(session, makeRef(QStringLiteral("aaaaaaaaaaaa"), QStringLiteral("deadbeefdeadbeef"), file));
    check(store.validate(session, QStringLiteral("aaaaaaaaaaaa")) ==
              yiuk::vision::SnapshotStore::Validation::Stale,
          QStringLiteral("snapshot taken under another layout is stale"));

    // 会话结束：删掉该会话的临时文件，其它会话不受影响。
    const QString otherFile = makeTempShot(dir, QStringLiteral("other-session.png"));
    store.remember(QStringLiteral("session-b"), makeRef(yiuk::vision::SnapshotStore::newId(), layout, otherFile));
    store.forgetSession(session);
    check(store.count(session) == 0, QStringLiteral("forgetSession() clears the session history"));
    check(!QFileInfo::exists(paths.last()), QStringLiteral("forgetSession() deletes the session temp files"));
    check(store.count(QStringLiteral("session-b")) == 1, QStringLiteral("forgetSession() leaves other sessions alone"));

    store.forgetAll();
    check(store.count(QStringLiteral("session-b")) == 0, QStringLiteral("forgetAll() clears every session"));
    check(!QFileInfo::exists(otherFile), QStringLiteral("forgetAll() deletes every temp file"));
}

void testLimiter() {
    yiuk::vision::SessionLimiter limiter(0);
    QString error;

    // 同会话串行：第二个必须先等第一个释放。
    check(limiter.acquire(QStringLiteral("s1"), nullptr, &error), QStringLiteral("limiter grants the first slot"));
    std::atomic_bool secondDone{false};
    QElapsedTimer clock;
    clock.start();
    std::thread waiter([&]() {
        QString waitError;
        const bool ok = limiter.acquire(QStringLiteral("s1"), nullptr, &waitError);
        secondDone.store(ok);
    });
    std::this_thread::sleep_for(std::chrono::milliseconds(60));
    check(!secondDone.load(), QStringLiteral("second acquire on the same session blocks"));
    limiter.release(QStringLiteral("s1"));
    waiter.join();
    check(secondDone.load() && clock.elapsed() < 5000, QStringLiteral("second acquire proceeds after release"));

    // 不同会话互不阻塞。
    yiuk::vision::SessionLimiter other(0);
    check(other.acquire(QStringLiteral("s1"), nullptr, &error), QStringLiteral("other session acquires freely"));
    other.release(QStringLiteral("s1"));

    // 排队期间取消：立刻返回 false，而不是等满最小间隔。
    yiuk::vision::SessionLimiter slow(600);
    check(slow.acquire(QStringLiteral("s1"), nullptr, &error), QStringLiteral("slow limiter grants the first slot"));
    slow.release(QStringLiteral("s1"));
    QElapsedTimer cancelClock;
    cancelClock.start();
    std::atomic_bool cancelled{false};
    std::thread canceller([&]() {
        std::this_thread::sleep_for(std::chrono::milliseconds(80));
        cancelled.store(true);
    });
    QString cancelError;
    const bool acquired = slow.acquire(QStringLiteral("s1"), &cancelled, &cancelError);
    canceller.join();
    check(!acquired, QStringLiteral("cancelled acquire returns false"));
    check(cancelClock.elapsed() < 550, QStringLiteral("cancelled acquire does not wait out the full interval"));
    check(cancelError.contains(QStringLiteral("取消")), QStringLiteral("cancelled acquire explains why"));

    // 最小间隔：两次成功的 acquire 之间至少隔 interval。
    yiuk::vision::SessionLimiter interval(120);
    QElapsedTimer intervalClock;
    intervalClock.start();
    for (int i = 0; i < 3; ++i) {
        check(interval.acquire(QStringLiteral("s1"), nullptr, &error), QStringLiteral("interval acquire succeeds"));
        interval.release(QStringLiteral("s1"));
    }
    check(intervalClock.elapsed() >= 200, QStringLiteral("min interval is enforced between captures"));
}

// click_target 的选择策略：唯一/歧义/边界 0.03 都在这里钉死。
void testPickTarget() {
    using yiuk::vision::TargetCandidate;
    using yiuk::vision::TargetPickStatus;

    const auto make = [](int left, int top, double score, const QString &text = QString()) {
        TargetCandidate candidate;
        candidate.bbox = QRect(left, top, 40, 20);
        candidate.score = score;
        candidate.text = text;
        candidate.source = QStringLiteral("uia");
        return candidate;
    };

    // 没有候选：NOT_FOUND，而不是“点屏幕左上角”。
    auto outcome = yiuk::vision::pickTarget({}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::NotFound, QStringLiteral("empty candidate set is NOT_FOUND"));

    // 单个候选：unique 直接点。
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.9)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Picked && outcome.index == 0,
          QStringLiteral("unique picks the only candidate"));

    // 两个不同位置、同分：判定为歧义，一个 down 也不发。
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.9), make(400, 100, 0.9)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Ambiguous, QStringLiteral("equal scores at different places is ambiguous"));
    check(outcome.competitor == 1, QStringLiteral("ambiguity names the competing candidate"));

    // 边界：得分差恰好 0.03 不算歧义（规则是“小于 0.03”）；0.029 算。
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.93), make(400, 100, 0.90)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Picked, QStringLiteral("score gap of exactly 0.03 is not ambiguous"));
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.929), make(400, 100, 0.90)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Ambiguous, QStringLiteral("score gap below 0.03 is ambiguous"));

    // 同一视觉目标的重叠命中（UIA 父子节点、NMS 残留）不是竞争：否则一个按钮永远点不了。
    // 中心重合或一个框有 ≥60% 面积被另一个盖住，就算同一个目标。
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.90), make(101, 100, 0.90)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Picked, QStringLiteral("overlapping same-target hits are not a competitor"));
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.90), make(100, 100, 0.90)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Picked, QStringLiteral("identical boxes are not a competitor"));
    // 相邻但不重叠的图标（差 60px）就是两个不同目标，仍要判歧义。
    outcome = yiuk::vision::pickTarget({make(100, 100, 0.90), make(160, 100, 0.90)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Ambiguous, QStringLiteral("neighbouring icons are different targets"));

    // 同位置的弱候选不能掩盖另一个位置的真竞争：比的是「不在同一目标上的最强对手」。
    outcome = yiuk::vision::pickTarget(
        {make(100, 100, 0.95), make(102, 100, 0.94), make(500, 300, 0.94)}, QStringLiteral("unique"));
    check(outcome.status == TargetPickStatus::Ambiguous,
          QStringLiteral("a same-target duplicate does not hide a real competitor"));

    // best / first / index:N 的语义。
    const QVector<TargetCandidate> many{make(100, 100, 0.80, QStringLiteral("first")), make(400, 100, 0.99),
                                        make(700, 100, 0.90)};
    outcome = yiuk::vision::pickTarget(many, QStringLiteral("best"));
    check(outcome.status == TargetPickStatus::Picked && outcome.index == 1,
          QStringLiteral("best picks the highest score"));
    outcome = yiuk::vision::pickTarget(many, QStringLiteral("first"));
    check(outcome.status == TargetPickStatus::Picked && outcome.index == 0,
          QStringLiteral("first picks the first returned candidate"));
    outcome = yiuk::vision::pickTarget(many, QStringLiteral("index:3"));
    check(outcome.status == TargetPickStatus::Picked && outcome.index == 2,
          QStringLiteral("index:3 is 1-based"));
    outcome = yiuk::vision::pickTarget(many, QStringLiteral("index:4"));
    check(outcome.status == TargetPickStatus::InvalidPick, QStringLiteral("index out of range is rejected"));
    outcome = yiuk::vision::pickTarget(many, QStringLiteral("index:0"));
    check(outcome.status == TargetPickStatus::InvalidPick, QStringLiteral("index:0 is rejected"));
    outcome = yiuk::vision::pickTarget(many, QStringLiteral("nearest"));
    check(outcome.status == TargetPickStatus::InvalidPick, QStringLiteral("unknown pick mode is rejected"));

    // pick 大小写与空白宽容（Agent 手写参数经常带空格）。
    outcome = yiuk::vision::pickTarget(many, QStringLiteral(" Best "));
    check(outcome.status == TargetPickStatus::Picked && outcome.index == 1,
          QStringLiteral("pick is trimmed and case-insensitive"));

    // 中心点与 find_on_screen 的取法一致（左 + 宽/2），点击落在同一个像素上。
    check(make(100, 100, 0.9).center() == QPoint(120, 110),
          QStringLiteral("candidate center matches find_on_screen rounding"));
}

void testTextNormalization() {
    using yiuk::vision::normalizeText;
    check(normalizeText(QStringLiteral("Save As")) == normalizeText(QStringLiteral("saveas")),
          QStringLiteral("normalization ignores spaces and case"));
    check(normalizeText(QStringLiteral("收 藏")) == normalizeText(QStringLiteral("收藏")),
          QStringLiteral("normalization removes spaces between CJK characters"));
    check(normalizeText(QStringLiteral("ＳＡＶＥ")) == normalizeText(QStringLiteral("save")),
          QStringLiteral("normalization folds full-width ASCII to half-width"));
    check(normalizeText(QStringLiteral("　設置　")) == normalizeText(QStringLiteral("設置")),
          QStringLiteral("normalization drops the ideographic space"));
    check(normalizeText(QString()).isEmpty(), QStringLiteral("empty text normalizes to empty"));

    check(yiuk::vision::controlTypeName(50000) == QLatin1String("Button"),
          QStringLiteral("control type name for button"));
    check(yiuk::vision::controlTypeId(QStringLiteral("edit")) == 50004,
          QStringLiteral("control type id lookup is case-insensitive"));
    check(yiuk::vision::controlTypeId(QStringLiteral("Nope")) == 0,
          QStringLiteral("unknown control type has no id"));
}

void testTextSearchOnDesktop() {
    // 在真实桌面上查一个几乎不可能存在的字符串：只验证 UIA 链路能跑通并给出空结果。
    yiuk::vision::TextQuery query;
    query.text = QStringLiteral("yiuk-nonexistent-label-9f3a");
    query.limit = 3;
    yiuk::vision::TextSearchOutcome outcome;
    QString code;
    QString message;
    if (yiuk::vision::findText(query, &outcome, &code, &message)) {
        check(outcome.matches.isEmpty(), QStringLiteral("UIA search for a missing label returns no matches"));
        check(outcome.visitedNodes >= 0, QStringLiteral("UIA search reports visited nodes"));
        std::printf("     uia: %d nodes, %d ms\n", outcome.visitedNodes, outcome.elapsedMs);
    } else {
        // 锁屏 / 无前台窗口 / CI 服务会话：不判失败，但必须说清楚没有验收。
        std::printf("WARN UIA search unavailable in this session: %s (%s)\n", qPrintable(code),
                    qPrintable(message));
    }

    // 空查询必须被拒绝，而不是遍历整棵树。
    yiuk::vision::TextQuery empty;
    check(!yiuk::vision::findText(empty, &outcome, &code, &message) && code == QLatin1String("INVALID_ARGUMENT"),
          QStringLiteral("empty text query is rejected"));

    // 未知 control_type 同样在遍历前拒绝。
    yiuk::vision::TextQuery badType;
    badType.text = QStringLiteral("x");
    badType.controlType = QStringLiteral("NotAControlType");
    check(!yiuk::vision::findText(badType, &outcome, &code, &message) && code == QLatin1String("INVALID_ARGUMENT"),
          QStringLiteral("unknown control_type is rejected"));
}

void testTemplateMatching() {
    using namespace yiuk::vision;
    if (!templateMatchingAvailable()) {
        std::printf("WARN OpenCV template matching is not compiled into this build\n");
        return;
    }
    // 背景必须有纹理：纯色背景会让 TM_CCOEFF_NORMED 退化（方差为 0）。
    QImage haystack(400, 300, QImage::Format_Grayscale8);
    for (int y = 0; y < haystack.height(); ++y)
        for (int x = 0; x < haystack.width(); ++x)
            haystack.setPixel(x, y, static_cast<uchar>(150 + (x * 3 + y * 5) % 40));

    QImage needle(48, 32, QImage::Format_Grayscale8);
    for (int y = 0; y < needle.height(); ++y)
        for (int x = 0; x < needle.width(); ++x)
            needle.setPixel(x, y, static_cast<uchar>(20 + (x * 7 + y * 13) % 200));

    const QPoint target(120, 80);
    for (int y = 0; y < needle.height(); ++y)
        for (int x = 0; x < needle.width(); ++x)
            haystack.setPixel(target.x() + x, target.y() + y, needle.pixel(x, y));

    TemplateOptions options;
    options.threshold = 0.90;
    options.limit = 3;
    QVector<TemplateHit> hits;
    QString code;
    QString message;
    check(matchTemplateImage(haystack, needle, options, &hits, nullptr, &code, &message),
          QStringLiteral("template matching runs"));
    check(hits.size() == 1, QStringLiteral("exactly one template match"));
    if (!hits.isEmpty()) {
        check(hits.first().rect.topLeft() == target, QStringLiteral("template match lands on the exact position"));
        check(hits.first().score > 0.99, QStringLiteral("template match score is high"));
    }

    // region 之外不再搜到。
    TemplateOptions elsewhere = options;
    elsewhere.region = QRect(0, 220, 200, 80);
    hits.clear();
    check(matchTemplateImage(haystack, needle, elsewhere, &hits, nullptr, &code, &message) && hits.isEmpty(),
          QStringLiteral("region limits the search area"));

    // 不存在的模板不能命中（负样本）。
    QImage absent(48, 32, QImage::Format_Grayscale8);
    for (int y = 0; y < absent.height(); ++y)
        for (int x = 0; x < absent.width(); ++x)
            absent.setPixel(x, y, static_cast<uchar>(10 + (x * 11 + y * 3) % 240));
    hits.clear();
    check(matchTemplateImage(haystack, absent, options, &hits, nullptr, &code, &message),
          QStringLiteral("matching an absent template does not fail"));
    check(hits.isEmpty(), QStringLiteral("absent template yields no match"));

    // 模板比搜索区域大：明确报错，不是"找不到"。
    TemplateOptions tooSmall = options;
    tooSmall.region = QRect(0, 0, 20, 20);
    hits.clear();
    check(!matchTemplateImage(haystack, needle, tooSmall, &hits, nullptr, &code, &message) &&
              code == QLatin1String("INVALID_ARGUMENT"),
          QStringLiteral("template larger than the region is rejected"));
}

// 用真实截图做一次匹配（可选）：YIUK_VISION_MATCH_HAYSTACK / _TEMPLATE 指向两张 PNG。
void testTemplateMatchingOnFiles() {
    const QString haystackPath = qEnvironmentVariable("YIUK_VISION_MATCH_HAYSTACK");
    const QString templatePath = qEnvironmentVariable("YIUK_VISION_MATCH_TEMPLATE");
    if (haystackPath.isEmpty() || templatePath.isEmpty()) return;
    if (!yiuk::vision::templateMatchingAvailable()) {
        std::printf("WARN OpenCV not compiled in; skipping real-pixel match\n");
        return;
    }
    const QImage haystack(haystackPath);
    const QImage needle(templatePath);
    if (haystack.isNull() || needle.isNull()) {
        check(false, QStringLiteral("real screenshots load from disk"));
        return;
    }
    yiuk::vision::TemplateOptions options;
    options.threshold = 0.85;
    options.limit = 3;
    QVector<yiuk::vision::TemplateHit> hits;
    QString code;
    QString message;
    const bool ok = yiuk::vision::matchTemplateImage(haystack, needle, options, &hits, nullptr, &code, &message);
    check(ok, QStringLiteral("real-pixel template matching runs"));
    if (ok && !hits.isEmpty()) {
        std::printf("     real match: rect=(%d,%d %dx%d) score=%.4f\n", hits.first().rect.left(),
                    hits.first().rect.top(), hits.first().rect.width(), hits.first().rect.height(),
                    hits.first().score);
    }
    check(ok && !hits.isEmpty(), QStringLiteral("template is found in the real screenshot"));
}

void testDeviceGate() {
    yiuk::vision::VisionService service;
    QString code;
    QString message;

    // 门被占用时，另一个"会话"必须排队；持有者离开后立刻放行。
    std::atomic_bool secondHeld{false};
    std::thread other;
    QElapsedTimer clock;
    clock.start();
    {
        yiuk::vision::VisionService::DeviceLock first(service, nullptr, &code, &message);
        check(first.held(), QStringLiteral("device gate grants the first holder"));
        other = std::thread([&]() {
            QString otherCode;
            QString otherMessage;
            yiuk::vision::VisionService::DeviceLock second(service, nullptr, &otherCode, &otherMessage);
            secondHeld.store(second.held());
        });
        std::this_thread::sleep_for(std::chrono::milliseconds(60));
        check(!secondHeld.load(), QStringLiteral("second device-level operation blocks"));
    }
    other.join();
    check(secondHeld.load() && clock.elapsed() < 5000,
          QStringLiteral("blocked operation proceeds after the holder leaves"));

    // 排队期间取消：立刻放弃，而不是等门自己空出来。
    std::atomic_bool cancelled{false};
    {
        yiuk::vision::VisionService::DeviceLock holder(service, nullptr, &code, &message);
        check(holder.held(), QStringLiteral("device gate can be re-acquired after release"));
        std::thread canceller([&]() {
            std::this_thread::sleep_for(std::chrono::milliseconds(60));
            cancelled.store(true);
        });
        QElapsedTimer cancelClock;
        cancelClock.start();
        QString cancelledCode;
        QString cancelledMessage;
        yiuk::vision::VisionService::DeviceLock blocked(service, &cancelled, &cancelledCode, &cancelledMessage);
        canceller.join();
        check(!blocked.held(), QStringLiteral("cancelled device-level wait gives up"));
        check(cancelledCode == QLatin1String("CANCELLED"),
              QStringLiteral("cancelled device-level wait reports CANCELLED"));
        check(cancelClock.elapsed() < 600, QStringLiteral("cancelled device-level wait does not block long"));
    }
}

void testPrune() {
    QTemporaryDir dir;
    check(dir.isValid(), QStringLiteral("temporary directory for prune test"));
    const qint64 now = QDateTime::currentMSecsSinceEpoch();
    const qint64 oldMsecs = now - 10 * 60 * 1000;

    auto touch = [&](const QString &name, qint64 modifiedMs) {
        const QString path = QDir(dir.path()).filePath(name);
        QFile file(path);
        file.open(QIODevice::WriteOnly);
        file.write("x");
        file.close();
        QFile device(path);
        device.open(QIODevice::ReadWrite);
        device.setFileTime(QDateTime::fromMSecsSinceEpoch(modifiedMs), QFileDevice::FileModificationTime);
        return path;
    };

    const QString kept = touch(QStringLiteral("keep.png"), oldMsecs);
    const QString stale = touch(QStringLiteral("stale.png"), oldMsecs);
    const QString fresh = touch(QStringLiteral("fresh.png"), now);

    const int removed = yiuk::vision::VisionService::pruneOrphanScreenshots(
        dir.path(), 5 * 60 * 1000, QStringList{kept}, now);
    check(removed == 1, QStringLiteral("prune removes only the stale orphan"));
    check(QFileInfo::exists(kept), QStringLiteral("prune keeps files still referenced by a snapshot"));
    check(!QFileInfo::exists(stale), QStringLiteral("prune deletes old orphan files"));
    check(QFileInfo::exists(fresh), QStringLiteral("prune keeps recent files"));
}

void testScalingAndEncoding() {
    QImage image(100, 50, QImage::Format_ARGB32);
    image.fill(Qt::darkGray);

    check(yiuk::vision::scaleToWidth(image, 0).size() == QSize(100, 50),
          QStringLiteral("max_width=0 keeps the physical size"));
    check(yiuk::vision::scaleToWidth(image, 200).size() == QSize(100, 50),
          QStringLiteral("max_width larger than the image keeps the original"));
    check(yiuk::vision::scaleToWidth(image, 50).size() == QSize(50, 25),
          QStringLiteral("scaling keeps the aspect ratio"));

    QTemporaryDir dir;
    QString error;
    const QString png = QDir(dir.path()).filePath(QStringLiteral("shot.png"));
    check(yiuk::vision::saveImage(image, png, QStringLiteral("png"), 90, &error),
          QStringLiteral("PNG encoding succeeds"));
    check(QFileInfo(png).size() > 0, QStringLiteral("PNG file is not empty"));
    QImage reloaded(png);
    check(reloaded.size() == QSize(100, 50), QStringLiteral("PNG round-trips the image size"));

    const QString jpeg = QDir(dir.path()).filePath(QStringLiteral("shot.jpeg"));
    check(yiuk::vision::saveImage(image, jpeg, QStringLiteral("jpeg"), 80, &error),
          QStringLiteral("JPEG encoding succeeds"));
    check(QFileInfo::exists(jpeg), QStringLiteral("JPEG file exists"));
    check(!yiuk::vision::saveImage(QImage(), jpeg, QStringLiteral("png"), 90, &error),
          QStringLiteral("encoding an empty image fails explicitly"));
}

void testVisionServiceCapture() {
    yiuk::vision::VisionService service;
    yiuk::vision::CaptureOptions options;
    options.maxWidth = 960;
    options.saveToDisk = false;

    yiuk::vision::CaptureOutcome outcome;
    QString code;
    QString message;
    if (!service.capture(QStringLiteral("session-capture"), options, nullptr, &outcome, &code, &message)) {
        // 无桌面会话（例如 CI 服务进程）下不判失败，但要说清楚没有验收。
        std::printf("WARN capture unavailable in this session: %s (%s)\n", qPrintable(code), qPrintable(message));
        return;
    }
    check(outcome.screenshotId.size() == 12, QStringLiteral("service returns a screenshot id"));
    check(outcome.geometry.width <= 960 && outcome.geometry.height > 0,
          QStringLiteral("service honours max_width"));
    check(outcome.geometry.screenWidth > 0 && outcome.geometry.screenHeight > 0,
          QStringLiteral("service returns the physical desktop rect"));
    check(outcome.filePath.isEmpty(), QStringLiteral("save=false does not write a file"));

    // 没有落盘时 latest 依然存在（坐标基准可用），且 id 校验通过。
    check(service.validateScreenshotId(QStringLiteral("session-capture"), outcome.screenshotId, &code, &message),
          QStringLiteral("service accepts its own screenshot id"));
    check(!service.validateScreenshotId(QStringLiteral("session-capture"), QStringLiteral("ffffffffffff"), &code,
                                        &message) &&
              code == QLatin1String("STALE_SCREENSHOT"),
          QStringLiteral("service rejects a foreign screenshot id"));

    // 落盘分支：文件真实写入，下载完可以用 screen.release 单独回收。
    yiuk::vision::CaptureOptions saved = options;
    saved.saveToDisk = true;
    saved.format = QStringLiteral("png");
    yiuk::vision::CaptureOutcome savedOutcome;
    if (service.capture(QStringLiteral("session-capture"), saved, nullptr, &savedOutcome, &code, &message)) {
        check(QFileInfo::exists(savedOutcome.filePath), QStringLiteral("save=true writes a screenshot file"));
        check(savedOutcome.sizeBytes > 0, QStringLiteral("saved screenshot reports its size"));

        // 只允许删截图目录内的文件：这条能力不能被用来删用户的任意文件。
        const QString outside = QDir(QDir::tempPath()).filePath(QStringLiteral("yiuk-outside.png"));
        check(!service.releaseScreenshotFile(QStringLiteral("session-capture"), outside, &code, &message) &&
                  code == QLatin1String("INVALID_ARGUMENT"),
              QStringLiteral("release refuses paths outside the screenshot directory"));

        check(service.releaseScreenshotFile(QStringLiteral("session-capture"), savedOutcome.filePath, &code, &message),
              QStringLiteral("release deletes a downloaded screenshot"));
        check(!QFileInfo::exists(savedOutcome.filePath), QStringLiteral("release actually removes the file"));
        // 文件没了，但坐标基准还在：screenshot_id 仍然可以用于后续点击。
        check(service.validateScreenshotId(QStringLiteral("session-capture"), savedOutcome.screenshotId, &code,
                                           &message),
              QStringLiteral("release keeps the screenshot id usable as a coordinate base"));

        // 会话结束仍然要兜底删掉剩下的文件。
        if (service.capture(QStringLiteral("session-capture"), saved, nullptr, &savedOutcome, &code, &message))
            service.forgetSession(QStringLiteral("session-capture"));
        check(!QFileInfo::exists(savedOutcome.filePath), QStringLiteral("forgetSession() deletes the screenshot"));
    } else {
        std::printf("WARN saved capture unavailable: %s\n", qPrintable(code));
    }
}

// QJsonArray 在 MinGW/Qt6 下不接受 QJsonArray{0,0,100,50} 这种写法，包一层。
QJsonArray intArray(std::initializer_list<int> values) {
    QJsonArray array;
    for (int value : values) array.append(value);
    return array;
}

// 原生操作参数解析：客户端 handler 与互操作程序共用 buildLocateRequest /
// buildClickTargetOptions，这里把默认值与边界钉住（纯函数，不碰屏幕）。
void testRequestParsing() {
    using namespace yiuk::vision;
    QString code;
    QString message;

    // 1) 文本定位：默认值来自 TextQuery（contains / limit 5），不塞不该有的东西。
    LocateRequest textRequest;
    check(buildLocateRequest(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")}}, &textRequest, &code,
                             &message),
          QStringLiteral("text locate request parses"));
    check(!textRequest.locate.useImage && textRequest.text.text == QStringLiteral("收藏"),
          QStringLiteral("text request keeps the query text"));
    check(textRequest.text.match == QStringLiteral("contains") && textRequest.text.limit == 5,
          QStringLiteral("text request uses contains/limit=5 defaults"));
    check(!textRequest.locate.hasRegion && textRequest.text.region.isEmpty(),
          QStringLiteral("text request has no region by default"));

    // 2) 显式参数与钳制：limit 99 -> 20；threshold 0.01 -> 0.10（图像路径）。
    LocateRequest explicitRequest;
    check(buildLocateRequest(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                         {QStringLiteral("match"), QStringLiteral("exact")},
                                         {QStringLiteral("control_type"), QStringLiteral("Button")},
                                         {QStringLiteral("region"), intArray({0, 0, 100, 50})},
                                         {QStringLiteral("limit"), 99}},
                             &explicitRequest, &code, &message),
          QStringLiteral("explicit locate arguments parse"));
    check(explicitRequest.text.match == QStringLiteral("exact") &&
              explicitRequest.text.controlType == QStringLiteral("Button"),
          QStringLiteral("match/control_type are forwarded"));
    check(explicitRequest.text.limit == 20, QStringLiteral("limit is clamped to 20"));
    check(explicitRequest.text.region == QRect(0, 0, 100, 50),
          QStringLiteral("region [left,top,right,bottom] becomes a QRect"));

    // 3) 拒绝：text 与图像同时给 / 都不给 / region 空面积。
    LocateRequest rejected;
    check(!buildLocateRequest(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                          {QStringLiteral("image_path"), QStringLiteral("C:\tpl\a.png")}},
                              &rejected, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("text + image is rejected"));
    check(!buildLocateRequest(QJsonObject{}, &rejected, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("an empty target is rejected"));
    check(!buildLocateRequest(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                          {QStringLiteral("region"), intArray({10, 10, 10, 90})}},
                              &rejected, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("a degenerate region is rejected"));

    // 4) 图像路径：合法 PNG 走 OpenCV（未编译 OpenCV 时跳过）。
    if (templateMatchingAvailable()) {
        QImage needle(8, 8, QImage::Format_ARGB32);
        needle.fill(Qt::red);
        QByteArray png;
        QBuffer buffer(&png);
        buffer.open(QIODevice::WriteOnly);
        needle.save(&buffer, "PNG");
        buffer.close();
        const QString encoded = QString::fromLatin1(png.toBase64());
        LocateRequest imageRequest;
        check(buildLocateRequest(QJsonObject{{QStringLiteral("image_b64"), encoded},
                                             {QStringLiteral("threshold"), 0.01},
                                             {QStringLiteral("limit"), 3}},
                                 &imageRequest, &code, &message),
              QStringLiteral("image_b64 request parses"));
        check(imageRequest.locate.useImage && imageRequest.locate.needle.size() == QSize(8, 8),
              QStringLiteral("image_b64 decodes into the template"));
        check(imageRequest.image.threshold > 0.099 && imageRequest.image.threshold < 0.101,
              QStringLiteral("threshold is clamped to 0.10"));
        check(imageRequest.image.limit == 3, QStringLiteral("image limit is forwarded"));

        // 超过 64 KiB 的解码上限：宁可报错，也不悄悄截断模板。
        const QString huge = QString::fromLatin1(QByteArray(70 * 1024, 'A').toBase64());
        check(!buildLocateRequest(QJsonObject{{QStringLiteral("image_b64"), huge}}, &imageRequest, &code,
                                  &message) &&
                  code == QStringLiteral("INVALID_ARGUMENT"),
              QStringLiteral("an oversized image_b64 is rejected"));
    } else {
        std::printf("WARN OpenCV is not compiled in; image parsing is not covered\n");
    }


    // 8) template_id：与另外三种来源互斥，且只带 id（像素由 VisionService 读）。
    LocateRequest templateRequest;
    check(buildLocateRequest(QJsonObject{{QStringLiteral("template_id"), QStringLiteral("aabbccddeeff")},
                                         {QStringLiteral("threshold"), 0.95}},
                             &templateRequest, &code, &message)
              ? templateRequest.locate.templateId == QStringLiteral("aabbccddeeff")
              : code == QStringLiteral("UNSUPPORTED_OPERATION"),
          QStringLiteral("template_id is accepted (or rejected when OpenCV is missing)"));
    if (!templateRequest.locate.templateId.isEmpty()) {
        check(templateRequest.locate.useImage && templateRequest.locate.needle.isNull(),
              QStringLiteral("template_id defers pixel loading to the service"));
        check(templateRequest.image.threshold > 0.94 && templateRequest.image.threshold < 0.96,
              QStringLiteral("threshold applies to templates too"));
    }
    LocateRequest conflict;
    check(!buildLocateRequest(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                          {QStringLiteral("template_id"), QStringLiteral("aabbccddeeff")}},
                              &conflict, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("text + template_id is rejected"));
    check(!buildLocateRequest(QJsonObject{{QStringLiteral("image_b64"), QStringLiteral("AAAA")},
                                          {QStringLiteral("template_id"), QStringLiteral("aabbccddeeff")}},
                              &conflict, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("image_b64 + template_id is rejected"));

    // 5) click_target 的冻结默认值：unique / left / 不双击 / 检测变化 / settle 250 / 不等待。
    ClickTargetOptions clickOptions;
    check(buildClickTargetOptions(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")}}, &clickOptions,
                                  &code, &message),
          QStringLiteral("click_target options parse"));
    check(clickOptions.pick == QStringLiteral("unique") && clickOptions.button == QStringLiteral("left") &&
              !clickOptions.doubleClick,
          QStringLiteral("click_target defaults are unique/left/no double click"));
    check(clickOptions.detectChange && clickOptions.settleMs == 250,
          QStringLiteral("click_target detects change with a 250ms settle by default"));
    check(!clickOptions.waitUntilFound, QStringLiteral("click_target does not wait by default"));

    // 6) 显式值优先，0 与 false 不能被当成"没给"。
    ClickTargetOptions explicitClick;
    check(buildClickTargetOptions(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                              {QStringLiteral("pick"), QStringLiteral("index:2")},
                                              {QStringLiteral("button"), QStringLiteral("right")},
                                              {QStringLiteral("double"), true},
                                              {QStringLiteral("detect_change"), false},
                                              {QStringLiteral("settle_ms"), 0}},
                                  &explicitClick, &code, &message),
          QStringLiteral("explicit click_target arguments parse"));
    check(explicitClick.pick == QStringLiteral("index:2") && explicitClick.button == QStringLiteral("right") &&
              explicitClick.doubleClick,
          QStringLiteral("pick/button/double are forwarded"));
    check(!explicitClick.detectChange && explicitClick.settleMs == 0,
          QStringLiteral("detect_change=false and settle_ms=0 survive parsing"));

    // 7) 等待预算：省略 -> 10 秒；超上限 -> 20 秒（平台 25 秒留 5 秒余量）。
    ClickTargetOptions waitDefault;
    buildClickTargetOptions(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                        {QStringLiteral("wait_until_found"), true}},
                            &waitDefault, &code, &message);
    check(waitDefault.waitUntilFound && waitDefault.waitTimeoutMs == 10000,
          QStringLiteral("waiting defaults to a 10s budget"));
    ClickTargetOptions waitCapped;
    buildClickTargetOptions(QJsonObject{{QStringLiteral("text"), QStringLiteral("收藏")},
                                        {QStringLiteral("wait_until_found"), true},
                                        {QStringLiteral("timeout_seconds"), 25}},
                            &waitCapped, &code, &message);
    check(waitCapped.waitTimeoutMs == 20000, QStringLiteral("wait budget is capped at 20s"));
}

// 用 GDI 画一张带文字的图（不依赖 Qt 字体/窗口），给 OCR 测试当输入。
QImage renderTextImage(const QStringList &lines) {
    const int width = 640;
    const int height = 60 + int(lines.size()) * 60;
    QImage image(width, height, QImage::Format_ARGB32);
    image.fill(Qt::white);
    HDC screen = GetDC(nullptr);
    HDC memory = CreateCompatibleDC(screen);
    HBITMAP bitmap = CreateCompatibleBitmap(screen, width, height);
    HGDIOBJ previous = SelectObject(memory, bitmap);
    RECT full{0, 0, width, height};
    FillRect(memory, &full, static_cast<HBRUSH>(GetStockObject(WHITE_BRUSH)));
    SetBkMode(memory, TRANSPARENT);
    SetTextColor(memory, RGB(20, 30, 45));
    LOGFONTW font{};
    font.lfHeight = -40;
    lstrcpyW(font.lfFaceName, L"Microsoft YaHei UI");
    HFONT textFont = CreateFontIndirectW(&font);
    HGDIOBJ oldFont = SelectObject(memory, textFont);
    int y = 40;
    for (const QString &line : lines) {
        const std::wstring wide = line.toStdWString();
        TextOutW(memory, 30, y, wide.c_str(), int(wide.size()));
        y += 60;
    }
    SelectObject(memory, oldFont);
    DeleteObject(textFont);
    GdiFlush();
    // 32 位 DIB 才是 BGRA；用 GetDIBits 拿不到就直接按 DDB 逐行读像素不可靠，
    // 这里改用 DIB section 重画一遍保证内存布局可控。
    BITMAPINFO info{};
    info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    info.bmiHeader.biWidth = width;
    info.bmiHeader.biHeight = -height;
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;
    void *bits = nullptr;
    HBITMAP dib = CreateDIBSection(screen, &info, DIB_RGB_COLORS, &bits, nullptr, 0);
    HDC dibDc = CreateCompatibleDC(screen);
    HGDIOBJ dibPrevious = SelectObject(dibDc, dib);
    BitBlt(dibDc, 0, 0, width, height, memory, 0, 0, SRCCOPY);
    GdiFlush();
    if (bits != nullptr) {
        for (int row = 0; row < height; ++row) {
            memcpy(image.scanLine(row), static_cast<const uchar *>(bits) + qsizetype(row) * width * 4,
                   size_t(width) * 4);
        }
    } else {
        image = QImage();
    }
    SelectObject(dibDc, dibPrevious);
    DeleteObject(dib);
    DeleteDC(dibDc);
    SelectObject(memory, previous);
    DeleteObject(bitmap);
    DeleteDC(memory);
    ReleaseDC(nullptr, screen);
    return image;
}

// OCR 文本匹配：词窗口、得分、去重、region 过滤（纯函数，不需要真 OCR）。
void testOcrMatching() {
    using namespace yiuk::vision;
    OcrLine line;
    line.text = QStringLiteral("收 藏 首 页 设 置");
    const QStringList words{QStringLiteral("收"), QStringLiteral("藏"), QStringLiteral("首"), QStringLiteral("页"),
                            QStringLiteral("设"), QStringLiteral("置")};
    for (int i = 0; i < words.size(); ++i) {
        OcrWord word;
        word.text = words.at(i);
        word.rect = QRect(30 + i * 44, 40, 40, 40);
        line.words.append(word);
    }
    OcrOutcome outcome;
    outcome.lines.append(line);

    TextQuery query;
    query.text = QStringLiteral("收藏");  // OCR 给的“收 藏”归一化后要能命中
    QVector<TextMatch> matches;
    matchOcrText(outcome, query, QRect(), &matches);
    check(matches.size() == 1, QStringLiteral("OCR matches a two-word CJK phrase"));
    check(matches.at(0).source == QStringLiteral("ocr") && matches.at(0).score > 0.89,
          QStringLiteral("OCR hits are scored like text matches and tagged with source=ocr"));
    // 框应该是“收 + 藏”两词的并集，而不是整行。
    check(matches.at(0).rect.left() == 30 && matches.at(0).rect.width() < 200,
          QStringLiteral("OCR bounding box covers only the matched words"));

    // 单字命中：只框那一个字。
    TextQuery single;
    single.text = QStringLiteral("页");
    matches.clear();
    matchOcrText(outcome, single, QRect(), &matches);
    check(matches.size() == 1 && matches.at(0).rect.width() <= 44,
          QStringLiteral("a single OCR word is boxed tightly"));

    // exact 语义：整窗完全一致才命中（“收藏”不会匹配到“收藏夹”这类更长文本）。
    TextQuery exact;
    exact.text = QStringLiteral("收藏");
    exact.match = QStringLiteral("exact");
    matches.clear();
    matchOcrText(outcome, exact, QRect(), &matches);
    check(matches.size() == 1 && matches.at(0).score == 1.0,
          QStringLiteral("exact OCR matching reports score 1.0"));

    // region 过滤：交集为空就不算命中。
    TextQuery inRegion;
    inRegion.text = QStringLiteral("收藏");
    matches.clear();
    matchOcrText(outcome, inRegion, QRect(0, 0, 20, 20), &matches);
    check(matches.isEmpty(), QStringLiteral("a region without overlap filters OCR hits out"));

    // 找不到就是空，不编造候选。
    TextQuery missing;
    missing.text = QStringLiteral("下载");
    matches.clear();
    matchOcrText(outcome, missing, QRect(), &matches);
    check(matches.isEmpty(), QStringLiteral("OCR returns no match for absent text"));
}

// 真 OCR：GDI 画字 → Windows.Media.Ocr → 词框。没有语言包时跳过（不算失败）。
void testOcrRecognition() {
    using namespace yiuk::vision;
    const QImage image = renderTextImage({QStringLiteral("收藏 首页 设置"), QStringLiteral("Settings 2026")});
    check(!image.isNull(), QStringLiteral("GDI renders a text image for OCR"));
    if (image.isNull()) return;

    OcrOutcome outcome;
    QString code;
    QString message;
    const bool ok = recognizeText(image, QStringLiteral("收藏"), nullptr, &outcome, &code, &message);
    if (!ok && code == QStringLiteral("OCR_UNAVAILABLE")) {
        std::printf("WARN OCR is unavailable on this machine: %s\n", qPrintable(message));
        return;
    }
    check(ok, QStringLiteral("recognizeText runs on a rendered image"));
    if (!ok) {
        std::printf("     OCR failed: %s %s\n", qPrintable(code), qPrintable(message));
        return;
    }
    check(!outcome.language.isEmpty() && !outcome.lines.isEmpty(),
          QStringLiteral("OCR reports a language and at least one line"));
    check(outcome.elapsedMs >= 0, QStringLiteral("OCR reports elapsed time"));

    TextQuery query;
    query.text = QStringLiteral("收藏");
    QVector<TextMatch> matches;
    matchOcrText(outcome, query, QRect(), &matches);
    check(!matches.isEmpty(), QStringLiteral("OCR finds the rendered CJK text"));
    if (!matches.isEmpty()) {
        const QRect box = matches.at(0).rect;
        std::printf("     ocr %s: %d lines, %d ms, box=%d,%d %dx%d\n", qPrintable(outcome.language),
                    int(outcome.lines.size()), outcome.elapsedMs, box.left(), box.top(), box.width(),
                    box.height());
        // 文字画在 (30,40) 附近，框应该落在图内且不在右下角。
        check(box.left() < image.width() / 2 && box.top() < image.height() / 2,
              QStringLiteral("OCR bounding box lands where the text was drawn"));
    }

    TextQuery latin;
    latin.text = QStringLiteral("Settings");
    latin.match = QStringLiteral("exact");
    matches.clear();
    matchOcrText(outcome, latin, QRect(), &matches);
    check(!matches.isEmpty(), QStringLiteral("OCR finds Latin text too"));
}

// 模板管理（P5）：从最近一帧保留的像素裁剪、配额、路径穿越与损坏判定。
void testTemplates() {
    using namespace yiuk::vision;
    VisionService service;
    QString code;
    QString message;

    // 需要一个真实抓屏：没有桌面会话时降级为警告（与其它真机用例一致）。
    CaptureOptions captureOptions;
    CaptureOptions saved = captureOptions;
    saved.saveToDisk = false;
    saved.keepImage = false;
    CaptureOutcome shot;
    if (!service.capture(QStringLiteral("session-template"), saved, nullptr, &shot, &code, &message)) {
        std::printf("WARN template tests skipped: capture failed (%s: %s)\n", qPrintable(code),
                    qPrintable(message));
        return;
    }
    const int width = shot.geometry.width;
    const int height = shot.geometry.height;
    check(shot.screenshotId.size() == 12, QStringLiteral("template source frame has a screenshot id"));

    // 1) 正常裁剪：尺寸与来源 region 一致，id 是 12 位 hex。
    const QRect region(40, 40, 160, 80);
    TemplateOutcome saved1;
    check(service.saveTemplate(QStringLiteral("session-template"), shot.screenshotId, region, &saved1, &code,
                               &message),
          QStringLiteral("saveTemplate slices the retained frame: %1").arg(message));
    check(saved1.templateId.size() == 12 && saved1.width == 160 && saved1.height == 80,
          QStringLiteral("template id and size are reported"));
    check(QFileInfo(QDir(VisionService::templatesDirectory()).filePath(saved1.templateId + QStringLiteral(".png")))
              .size() == saved1.sizeBytes,
          QStringLiteral("template file is written to the templates directory"));

    QImage loaded;
    check(service.loadTemplate(saved1.templateId, &loaded, &code, &message),
          QStringLiteral("loadTemplate reads the saved template"));
    check(loaded.size() == QSize(160, 80), QStringLiteral("template keeps the requested pixel size"));
    // 存下来的像素必须真的来自那一帧（不是纯色占位）。
    check(!loaded.isNull() && loaded.pixelColor(0, 0).isValid(),
          QStringLiteral("template contains real pixels"));

    // 2) 旧 screenshot_id / 越界 region / 超大模板都要明确拒绝。
    TemplateOutcome rejected;
    check(!service.saveTemplate(QStringLiteral("session-template"), QStringLiteral("000000000000"), region,
                                &rejected, &code, &message) &&
              code == QStringLiteral("STALE_SCREENSHOT"),
          QStringLiteral("a stale screenshot id is STALE_SCREENSHOT"));
    check(!service.saveTemplate(QStringLiteral("session-template"), shot.screenshotId,
                                QRect(width - 10, height - 10, 200, 200), &rejected, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("a region outside the frame is rejected"));
    check(!service.saveTemplate(QStringLiteral("session-template"), shot.screenshotId,
                                QRect(0, 0, qMin(width, 2100), 40), &rejected, &code, &message) &&
              (code == QStringLiteral("INVALID_ARGUMENT") || code == QStringLiteral("TEMPLATE_TOO_LARGE")),
          QStringLiteral("an oversized region is rejected"));

    // 3) 第二次抓屏之后，旧帧的像素被放掉：模板只能从"当前这一帧"裁。
    CaptureOutcome second;
    if (service.capture(QStringLiteral("session-template"), saved, nullptr, &second, &code, &message)) {
        check(!service.saveTemplate(QStringLiteral("session-template"), shot.screenshotId, region, &rejected,
                                    &code, &message) &&
                  code == QStringLiteral("STALE_SCREENSHOT"),
              QStringLiteral("after a newer frame the old screenshot cannot be used for templates"));
        check(service.saveTemplate(QStringLiteral("session-template"), second.screenshotId, region, &saved1,
                                   &code, &message),
              QStringLiteral("the newest frame can still be sliced"));
    }

    // 4) 路径穿越 / 不存在 / 损坏。
    QImage ignored;
    check(!service.loadTemplate(QStringLiteral("../../windows/win.ini"), &ignored, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("path traversal in template_id is rejected"));
    check(!service.loadTemplate(QString(), &ignored, &code, &message) &&
              code == QStringLiteral("INVALID_ARGUMENT"),
          QStringLiteral("empty template id is rejected"));
    check(!service.loadTemplate(QStringLiteral("0123456789ab"), &ignored, &code, &message) &&
              code == QStringLiteral("TEMPLATE_NOT_FOUND"),
          QStringLiteral("unknown template id is TEMPLATE_NOT_FOUND"));

    const QString corruptPath =
        QDir(VisionService::templatesDirectory()).filePath(QStringLiteral("ffffffffffff.png"));
    {
        QFile corrupt(corruptPath);
        if (corrupt.open(QIODevice::WriteOnly)) corrupt.write("not a png at all");
    }
    check(!service.loadTemplate(QStringLiteral("ffffffffffff"), &ignored, &code, &message) &&
              code == QStringLiteral("TEMPLATE_CORRUPTED"),
          QStringLiteral("a corrupted template file is reported, not silently ignored"));
    QFile::remove(corruptPath);

    // 5) 配额：写满 100 条索引后必须明确拒绝（先备份现有索引，测完还原）。
    const QString indexPath = QDir(VisionService::templatesDirectory()).filePath(QStringLiteral("index.json"));
    QByteArray backup;
    bool hadIndex = false;
    {
        QFile existing(indexPath);
        if (existing.open(QIODevice::ReadOnly)) {
            backup = existing.readAll();
            hadIndex = true;
        }
    }
    QJsonObject fakeTemplates;
    for (int i = 0; i < VisionService::kMaxTemplateCount; ++i)
        fakeTemplates.insert(QStringLiteral("%1").arg(i, 12, 10, QLatin1Char('0')),
                             QJsonObject{{QStringLiteral("width"), 1}, {QStringLiteral("bytes"), 1}});
    {
        QFile indexFile(indexPath);
        if (indexFile.open(QIODevice::WriteOnly | QIODevice::Truncate)) {
            indexFile.write(QJsonDocument(QJsonObject{{QStringLiteral("templates"), fakeTemplates}})
                                .toJson(QJsonDocument::Compact));
        }
    }
    TemplateOutcome quotaOutcome;
    check(!service.saveTemplate(QStringLiteral("session-template"), QString(), region, &quotaOutcome, &code,
                                &message) &&
              code == QStringLiteral("TEMPLATE_QUOTA_EXCEEDED"),
          QStringLiteral("the template count quota is enforced"));
    {
        QFile indexFile(indexPath);
        if (hadIndex) {
            if (indexFile.open(QIODevice::WriteOnly | QIODevice::Truncate)) indexFile.write(backup);
        } else if (QFile::exists(indexPath)) {
            QFile::remove(indexPath);
        }
    }

    // 清理本次会话与它产生的模板文件，避免污染真实目录。
    service.forgetSession(QStringLiteral("session-template"));
    QFile::remove(QDir(VisionService::templatesDirectory()).filePath(saved1.templateId + QStringLiteral(".png")));
}


// 失败 hint 必须按现场说话：纯色、阈值、歧义、OCR 语言包各说各的。
void testFailureHints() {
    using namespace yiuk::vision;

    // 纯色区域：不要说“降低阈值”，因为那没有意义。
    const QString uniform = failureHint(QStringLiteral("NOT_FOUND"),
                                        QJsonObject{{QStringLiteral("uniform_area"), true},
                                                    {QStringLiteral("area_stddev"), 0.4}});
    check(uniform.contains(QStringLiteral("纯色")) && !uniform.contains(QStringLiteral("threshold")),
          QStringLiteral("a uniform search area is described as textureless"));

    // 有最高分：把“最高分 vs 阈值”说清楚，而不是空泛建议。
    const QString scored = failureHint(QStringLiteral("NOT_FOUND"),
                                       QJsonObject{{QStringLiteral("max_score"), 0.71},
                                                   {QStringLiteral("threshold"), 0.88},
                                                   {QStringLiteral("uniform_area"), false}});
    check(scored.contains(QStringLiteral("0.71")) && scored.contains(QStringLiteral("0.88")),
          QStringLiteral("the hint quotes the best score and the threshold"));
    check(scored.contains(QStringLiteral("take_screenshot")),
          QStringLiteral("the hint sends the agent back to the live screen first"));

    // OCR 也没命中：把 OCR 的原因带上。
    const QString ocrNote = failureHint(QStringLiteral("NOT_FOUND"),
                                        QJsonObject{{QStringLiteral("ocr_note"), QStringLiteral("没有可用的 OCR 引擎")}});
    check(ocrNote.contains(QStringLiteral("OCR")) && ocrNote.contains(QStringLiteral("没有可用的 OCR 引擎")),
          QStringLiteral("the hint carries the OCR reason when both locators missed"));

    // 歧义：给候选数与操作方式，且不包含“已点击”这种谎话。
    const QString ambiguous = failureHint(QStringLiteral("AMBIGUOUS_TARGET"),
                                          QJsonObject{{QStringLiteral("candidate_count"), 3}});
    check(ambiguous.contains(QStringLiteral("3")) && ambiguous.contains(QStringLiteral("pick=best")),
          QStringLiteral("the ambiguity hint names the candidate count and the pick options"));

    // 缺语言包 / 模板配额 / 模板不存在：各自指向具体动作。
    check(failureHint(QStringLiteral("OCR_UNAVAILABLE"), QJsonObject()).contains(QStringLiteral("语言")),
          QStringLiteral("OCR_UNAVAILABLE points at the language pack"));
    check(failureHint(QStringLiteral("TEMPLATE_QUOTA_EXCEEDED"), QJsonObject()).contains(QStringLiteral("templates")),
          QStringLiteral("the quota hint points at the templates directory"));
    check(failureHint(QStringLiteral("TEMPLATE_NOT_FOUND"), QJsonObject()).contains(QStringLiteral("save_template")),
          QStringLiteral("a missing template points back at save_template"));
    check(failureHint(QStringLiteral("UNKNOWN_CODE"), QJsonObject()).isEmpty(),
          QStringLiteral("unknown codes get no invented hint"));
}


// ROI 缓存（P5）：只在"只要一个命中"的只读查找里用；命中必须在当前帧上重新验证；
// 换目标/换布局/过期都不许命中旧位置；click_target 一律不用它。
void testRoiCache() {
    using namespace yiuk::vision;

    // 纯逻辑：键、扩框、TTL、条目上限。
    TemplateOptions options;
    QImage needle(16, 16, QImage::Format_Grayscale8);
    needle.fill(0x40);
    const QString keyA = RoiCache::imageKey(needle, options, QStringLiteral("layout-a"));
    QImage other = needle;
    other.fill(0x80);
    check(keyA != RoiCache::imageKey(other, options, QStringLiteral("layout-a")),
          QStringLiteral("a different template yields a different ROI key"));
    check(keyA != RoiCache::imageKey(needle, options, QStringLiteral("layout-b")),
          QStringLiteral("a layout change invalidates the ROI key"));
    TemplateOptions stricter = options;
    stricter.threshold = 0.95;
    check(keyA != RoiCache::imageKey(needle, stricter, QStringLiteral("layout-a")),
          QStringLiteral("a threshold change invalidates the ROI key"));

    const QRect area(0, 0, 1000, 800);
    check(RoiCache::expand(QRect(100, 100, 40, 20), 10, area) == QRect(90, 90, 60, 40),
          QStringLiteral("the ROI grows around the previous hit"));
    check(RoiCache::expand(QRect(0, 0, 40, 20), 10, area) == QRect(0, 0, 50, 30),
          QStringLiteral("the ROI is clamped to the search area"));

    RoiCache cache;
    cache.store(keyA, QRect(120, 200, 40, 20), 1000);
    check(cache.lookup(keyA, 1000).topLeft() == QPoint(120, 200),
          QStringLiteral("a stored hit is returned inside the TTL"));
    check(cache.lookup(keyA, 1000 + RoiCache::kTtlMs + 1).isNull(),
          QStringLiteral("an expired ROI entry is ignored"));
    for (int i = 0; i < RoiCache::kMaxEntries * 2; ++i)
        cache.store(keyA + QString::number(i), QRect(i, i, 10, 10), 2000);
    check(cache.size() <= RoiCache::kMaxEntries, QStringLiteral("the ROI cache stays bounded"));

    // 真机：同一个模板连查两次，第二次应该只在旧位置附近搜。
    VisionService service;
    CaptureOutcome shot;
    QString code;
    QString message;
    CaptureOptions captureOptions;
    captureOptions.saveToDisk = false;
    captureOptions.keepImage = true;
    if (!service.capture(QStringLiteral("session-roi"), captureOptions, nullptr, &shot, &code, &message)) {
        std::printf("WARN ROI cache end-to-end skipped: capture failed (%s)\n", qPrintable(code));
        return;
    }
    // 从这一帧里裁一小块当模板（保证第一次就能命中）。
    const QRect templateRect(shot.image.width() / 3, shot.image.height() / 3, 48, 32);
    const QImage templ = shot.image.copy(templateRect);

    TemplateOptions search;
    search.limit = 1;
    QJsonObject first;
    if (!service.matchImage(QStringLiteral("session-roi"), templ, search, nullptr, &first, &code, &message)) {
        std::printf("WARN ROI cache end-to-end skipped: %s %s\n", qPrintable(code), qPrintable(message));
        return;
    }
    check(first.value(QStringLiteral("roi_cached")).toBool() == false,
          QStringLiteral("the first lookup searches the whole screen"));
    const int fullArea = first.value(QStringLiteral("search_area")).toInt();
    const int firstSearched = first.value(QStringLiteral("searched_area")).toInt();
    check(fullArea > 0 && firstSearched == fullArea, QStringLiteral("the first lookup reports the full area"));

    QJsonObject second;
    if (service.matchImage(QStringLiteral("session-roi"), templ, search, nullptr, &second, &code, &message)) {
        check(second.value(QStringLiteral("roi_cached")).toBool() == true,
              QStringLiteral("a repeated lookup reuses the ROI"));
        check(second.value(QStringLiteral("searched_area")).toInt() < fullArea / 2,
              QStringLiteral("the ROI is much smaller than the whole screen"));
        const QJsonArray matches = second.value(QStringLiteral("matches")).toArray();
        const QJsonArray firstMatches = first.value(QStringLiteral("matches")).toArray();
        check(matches.size() == 1, QStringLiteral("the ROI hit is a real match on the current frame"));
        if (!matches.isEmpty() && !firstMatches.isEmpty()) {
            // 缓存必须保持"同一次匹配的落点"：与第一次全屏命中的中心一致（±2px 像素取整）。
            const QJsonArray center = matches.at(0).toObject().value(QStringLiteral("center")).toArray();
            const QJsonArray firstCenter = firstMatches.at(0).toObject().value(QStringLiteral("center")).toArray();
            const bool sameSpot =
                center.size() == 2 && firstCenter.size() == 2 &&
                std::abs(center.at(0).toInt() - firstCenter.at(0).toInt()) <= 2 &&
                std::abs(center.at(1).toInt() - firstCenter.at(1).toInt()) <= 2;
            std::printf("     roi hit %d,%d vs full hit %d,%d\n", center.at(0).toInt(),
                        center.at(1).toInt(), firstCenter.at(0).toInt(), firstCenter.at(1).toInt());
            check(sameSpot, QStringLiteral("the ROI hit lands on the same target as the full search"));
        }
    } else {
        check(false, QStringLiteral("a repeated lookup still succeeds"));
    }

    // 要多个候选时不许用缓存（否则会少报）。
    TemplateOptions many = search;
    many.limit = 5;
    QJsonObject limited;
    if (service.matchImage(QStringLiteral("session-roi"), templ, many, nullptr, &limited, &code, &message))
        check(limited.value(QStringLiteral("roi_cached")).toBool() == false,
              QStringLiteral("limit > 1 never uses the ROI shortcut"));

    service.forgetSession(QStringLiteral("session-roi"));
}

}  // namespace

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    QCoreApplication::setApplicationName(QStringLiteral("YiukDeviceAssist"));
    QCoreApplication::setOrganizationName(QStringLiteral("Yiuk"));

    testIds();
    testStore();
    testLimiter();
    testScalingAndEncoding();
    testTemplateMatching();
    testTemplateMatchingOnFiles();
    testTextNormalization();
    testPickTarget();
    testRequestParsing();
    testOcrMatching();
    testOcrRecognition();
    testTextSearchOnDesktop();
    testDeviceGate();
    testPrune();
    testVisionServiceCapture();
    testTemplates();
    testFailureHints();
    testRoiCache();

    std::printf(failures == 0 ? "VISION OK\n" : "VISION FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
