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
#include "vision/visionservice.h"

#include <QCoreApplication>
#include <QDir>
#include <QElapsedTimer>
#include <QFile>
#include <QFileInfo>
#include <QImage>
#include <QTemporaryDir>

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

}  // namespace

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    QCoreApplication::setApplicationName(QStringLiteral("YiukDeviceAssist"));
    QCoreApplication::setOrganizationName(QStringLiteral("Yiuk"));

    testIds();
    testStore();
    testLimiter();
    testScalingAndEncoding();
    testDeviceGate();
    testPrune();
    testVisionServiceCapture();

    std::printf(failures == 0 ? "VISION OK\n" : "VISION FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
