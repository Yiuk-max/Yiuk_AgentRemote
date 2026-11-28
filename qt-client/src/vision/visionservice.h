#pragma once

#include "devicegate.h"
#include "sessionlimiter.h"
#include "snapshotstore.h"
#include "templatematcher.h"
#include "textlocator.h"

#include <QObject>
#include <QString>

#include <atomic>

namespace yiuk::vision {

// 一次抓屏的参数。由原生操作 screen.capture 的 arguments 解析而来。
struct CaptureOptions {
    // 输出图像的最大宽度（像素）；0 = 保持物理尺寸。上限见 kMaxOutputWidth。
    int maxWidth = 0;
    // 只接受 "png" / "jpeg"。
    QString format = QStringLiteral("png");
    // 是否落盘。false 时只回几何（视觉查找不需要图片时用）。
    bool saveToDisk = true;
    // 是否把这一帧像素留在内存里（模板匹配要直接拿当前画面，不落盘、不下载）。
    bool keepImage = false;
    int jpegQuality = 90;
};

struct CaptureOutcome {
    QString screenshotId;
    QString filePath;
    qint64 sizeBytes = 0;
    ScreenGeometry geometry;
    QString layoutVersion;
    // 仅当 CaptureOptions::keepImage 为真时填充。
    QImage image;
};

// VisionService 是客户端内所有"重操作"的唯一入口：
//
//   * 抓屏（screen.capture，后续 take_screenshot 的原生分支）；
//   * 设备级串行门（DeviceGate）+ 会话级互斥 + 500ms 最小间隔（SessionLimiter）；
//   * 统一快照登记（SnapshotStore）：screenshot_id、坐标基准、临时文件生命周期。
//
// 视觉定位（P3）与鼠标动作（P4）都从这里取坐标基准，不允许另建一套 ID。
class VisionService final : public QObject {
public:
    static constexpr int kMaxOutputWidth = 4096;

    // 设备级重操作/输入串行门的 RAII 持有者。
    //
    // 用法：先构造，held() 为 false 时直接返回错误（code 已写好），
    // 析构时自动放门。抓屏内部会自己取门，调用方**不要**在持有它时再调 capture()。
    class DeviceLock {
    public:
        DeviceLock(VisionService &service, const std::atomic_bool *cancelled, QString *code, QString *message);
        ~DeviceLock();
        DeviceLock(const DeviceLock &) = delete;
        DeviceLock &operator=(const DeviceLock &) = delete;
        bool held() const { return held_; }

    private:
        VisionService *service_ = nullptr;
        bool held_ = false;
    };

    // 一次完整的点击：设备门 -> 坐标换算 -> (可选)点击前后局部采样 -> 结果。
    // 真实客户端与互操作程序都用这一份，避免"设备端一边采样、测试替身不采样"。
    bool clickWithOptions(const QString &sessionId, const QString &screenshotId, int x, int y,
                          const QString &button, bool doubleClick, bool detectChange, int settleMs,
                          const std::atomic_bool *cancelled, QJsonObject *data, QString *code, QString *message);

    // 一次完整的模板匹配：抓一帧（不落盘）-> OpenCV 灰度匹配 -> 换算成截图坐标 -> 组装 data。
    // needle 是模板（PNG/JPEG 解码后的 QImage）；region 用新这一帧的坐标。
    bool matchImage(const QString &sessionId, const QImage &needle, const TemplateOptions &options,
                    const std::atomic_bool *cancelled, QJsonObject *data, QString *code, QString *message);

    // 一次完整的文本查找：确保坐标基准 -> UIA 查找 -> 换算成截图坐标 -> 组装 data。
    // data 里包含截图几何、screenshot_id 与 matches，可直接作为原生操作的业务数据；
    // 只有 text 定位放在这里，图像匹配（OpenCV）等实现后再扩展。
    bool searchText(const QString &sessionId, const TextQuery &query, const std::atomic_bool *cancelled,
                    QJsonObject *data, QString *code, QString *message);

    // 把物理屏幕矩形换算成最近一张截图的坐标（vision.find 的返回坐标）。
    // 没有截图、或几何无效时返回 false，并通过 code 说明原因。
    bool screenRectToScreenshot(const QString &sessionId, const QRect &screenRect, QRect *out, SnapshotRef *ref,
                                QString *code, QString *message) const;

    // 删除一张已下载完的临时截图：只删文件，保留快照元数据（screenshot_id 仍然有效）。
    // 只接受 screenshots 目录内的路径，避免把任意文件删除变成远程能力。
    bool releaseScreenshotFile(const QString &sessionId, const QString &path, QString *code, QString *message);

    // 清理截图目录里不再被任何快照引用的旧文件（客户端崩溃/断电后残留）。
    // 纯函数，便于测试：返回删除的文件数。
    static int pruneOrphanScreenshots(const QString &directory, qint64 olderThanMs, const QStringList &keep,
                                      qint64 nowMs);

    explicit VisionService(QObject *parent = nullptr);

    // 原生截图的落盘目录：%LOCALAPPDATA%/Yiuk/DeviceAssist/screenshots
    static QString screenshotsDirectory();

    // capture 完成一次抓屏。失败时写入 code（CANCELLED / SCREENSHOT_FAILED /
    // SCREENSHOT_SAVE_FAILED）与 message，并返回 false。
    bool capture(const QString &sessionId, const CaptureOptions &options, const std::atomic_bool *cancelled,
                 CaptureOutcome *outcome, QString *code, QString *message);

    // 校验调用方给的 screenshot_id 是否仍是坐标基准。
    // id 为空表示"沿用最新一张"：有截图就通过，没有截图返回 SCREENSHOT_REQUIRED。
    bool validateScreenshotId(const QString &sessionId, const QString &id, QString *code,
                              QString *message) const;

    bool latestScreenshot(const QString &sessionId, SnapshotRef *out) const;

    // 把截图坐标换算成物理屏幕坐标（与 Go 侧 screenPoint 的取整一致）。
    // screenshot_id 为空表示沿用最新一张；过期/未知/越界都返回 false 并写入 code。
    bool toScreenPoint(const QString &sessionId, const QString &screenshotId, int x, int y, int *screenX,
                       int *screenY, QString *code, QString *message) const;

    // 会话结束：删除临时截图并让该会话的旧 id 失效。
    void forgetSession(const QString &sessionId);
    // 桌面布局变化：所有会话的旧 id 立即失效。
    void forgetAll();

    const SnapshotStore &store() const { return store_; }

private:
    DeviceGate deviceGate_;
    SessionLimiter limiter_;
    SnapshotStore store_;
    // 最近一次观察到的桌面布局；变化时清空所有快照。
    QString layoutVersion_;
};

}  // namespace yiuk::vision
