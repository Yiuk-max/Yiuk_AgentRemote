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

// 一条候选命中（截图坐标，右下开区间）。UIA 与 OpenCV 两条来源统一成这一种形状，
// 这样"选哪一个、是否歧义"只写一份。
struct TargetCandidate {
    QRect bbox;
    double score = 0;
    QString text;
    QString source;
    QString controlType;
    QPoint center() const { return QPoint(bbox.left() + bbox.width() / 2, bbox.top() + bbox.height() / 2); }
};

// 歧义判据（P4 冻结）：第一名与**另一个视觉目标**的最强对手得分差 < 0.03 即视为歧义。
// 同一个目标的重叠命中（UIA 父子节点、NMS 残留：中心重合或一个框有 ≥60% 面积被另一个盖住）
// 不算竞争，否则一个按钮永远点不成。
extern const double kAmbiguityScoreGap;

enum class TargetPickStatus {
    Picked,
    NotFound,
    Ambiguous,
    InvalidPick,
};

struct TargetPickOutcome {
    TargetPickStatus status = TargetPickStatus::NotFound;
    // Picked 时被选中的下标；Ambiguous 时是第一名；-1 表示没有候选。
    int index = -1;
    // 歧义时位置不同的最强对手下标，其余情况 -1。
    int competitor = -1;
    QString message;
};

// 按 pick 选择要点击的候选。纯函数（不碰系统 API），便于把各分支与 0.03 边界钉在测试里。
//
// pick：unique（默认，命中多个不同位置的近似并列结果时判歧义）/ best（得分最高）/
//       first（返回顺序第一个：UIA 是阅读顺序、图像是得分降序）/ index:N（第 N 个，1 起）。
TargetPickOutcome pickTarget(const QVector<TargetCandidate> &candidates, const QString &pick);

// 一次模板保存的结果：id 与像素尺寸，region 是它在截图坐标系里的来源位置。
struct TemplateOutcome {
    QString templateId;
    int width = 0;
    int height = 0;
    qint64 sizeBytes = 0;
    QRect region;
};

// 一次「定位 + 点击」的配置：定位来源二选一（text 或 needle），点击参数与 click 同义。
struct ClickTargetOptions {
    // 定位来源。useImage 为真时用 needle + image，否则用 text（UIA）。
    bool useImage = false;
    QImage needle;
    // 引用设备上的模板；needle 为空且有 id 时由 clickTarget 自己读出来（含格式/存在性/损坏判定）。
    QString templateId;
    TemplateOptions image;
    TextQuery text;
    // unique（默认）| best | first | index:N
    QString pick = QStringLiteral("unique");
    QString button = QStringLiteral("left");
    bool doubleClick = false;
    // 与 click 一致：可选点击前后的局部采样；click_target 默认开（规格要求）。
    bool detectChange = true;
    int settleMs = 250;
    // 等目标出现：每 500ms 一轮，**轮间释放设备门**（不能为了等一个按钮
    // 把整台设备锁住 20 秒）。找到的那一轮在同一个门内直接点完。
    bool waitUntilFound = false;
    int waitTimeoutMs = 0;
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
        // 提前放门（例如“等目标出现”的轮间）：析构时不会再重复放。
        void release();

    private:
        VisionService *service_ = nullptr;
        bool held_ = false;
    };

    // 一次完整的点击：设备门 -> 坐标换算 -> (可选)点击前后局部采样 -> 结果。
    // 真实客户端与互操作程序都用这一份，避免"设备端一边采样、测试替身不采样"。
    bool clickWithOptions(const QString &sessionId, const QString &screenshotId, int x, int y,
                          const QString &button, bool doubleClick, bool detectChange, int settleMs,
                          const std::atomic_bool *cancelled, QJsonObject *data, QString *code, QString *message);

    // 一次「找到就点」的事务：设备门 -> 抓一帧 -> 定位 -> 选择 -> 点击 -> 变化采样。
    // 每一轮都持门：定位与点击之间不会被别的会话插进来抓屏/点鼠标（等待时轮间放门）。
    //
    // 找不到 / 歧义时**一个 down 事件都不发**：失败返回 false 并写 code/message，
    // 同时把现场（候选、得分、阈值）写进 *data，供上层放进错误信封。
    bool clickTarget(const QString &sessionId, const ClickTargetOptions &options,
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

    // 从**最近一张快照保留的像素**裁剪出模板并存盘（vision.save_template）。
    //
    // 不会为了这次调用重新抓屏：像素已被回收（旧截图 id / 本次未保存像素）时明确
    // 返回 STALE_SCREENSHOT，让调用方重新截图——悄悄抓一张新画面冒充"你刚才看到的那一帧"
    // 会让模板与坐标基准错位。
    bool saveTemplate(const QString &sessionId, const QString &screenshotId, const QRect &region,
                      TemplateOutcome *outcome, QString *code, QString *message);

    // 读回模板像素；id 必须是 12 位小写 hex（拒绝路径穿越）。
    bool loadTemplate(const QString &templateId, QImage *image, QString *code, QString *message) const;

    // 模板目录：<程序目录>data	emplates（绿色版）。
    static QString templatesDirectory();

    // 配额（P5 冻结）：单个 4 MiB、总数 100、总大小 32 MiB；超了明确报错，不静默驱逐。
    static constexpr int kMaxTemplateCount = 100;
    static constexpr qint64 kMaxTemplateBytes = 4 << 20;
    static constexpr qint64 kMaxTemplatesTotalBytes = 32 << 20;

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
    // 已持有设备门时的抓屏：设备门由调用方持有（click_target 要在同一把门下完成
    // 抓屏 + 定位 + 点击，不能在中间放门）。capture() 就是「取门 + 这里」。
    bool captureLocked(const QString &sessionId, const CaptureOptions &options,
                       const std::atomic_bool *cancelled, CaptureOutcome *outcome, QString *code, QString *message);

    // 已持有设备门时的点击（坐标换算、前后采样）。clickWithOptions 与 clickTarget 共用。
    bool clickPointLocked(const QString &sessionId, const QString &screenshotId, int x, int y,
                          const QString &button, bool doubleClick, bool detectChange, int settleMs,
                          const std::atomic_bool *cancelled, QJsonObject *data, QString *code, QString *message);

    DeviceGate deviceGate_;
    SessionLimiter limiter_;
    SnapshotStore store_;
    // 最近一次观察到的桌面布局；变化时清空所有快照。
    QString layoutVersion_;
};

}  // namespace yiuk::vision
