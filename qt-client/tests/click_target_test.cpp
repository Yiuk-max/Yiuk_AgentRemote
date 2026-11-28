// click_target 真机验收（需要一个可交互的桌面会话）。
//
// 与 vision_test 的区别：这里放的是**真窗口**，判据是窗口自己收到了点击事件
// （WM_COMMAND / BN_CLICKED），而不是"SendInput 返回成功"。覆盖：
//   1. 没有命中时返回 NOT_FOUND，且一次 down 都不发；
//   2. 两个同文本按钮位于不同位置时返回 AMBIGUOUS_TARGET，一次 down 都不发；
//   3. pick=index:N 真的点到第 N 个，pick=best 点到唯一那个；
//   4. 点击落点等于命中框中心（与 find_on_screen 的 center 同一算法）；
//   5. 等目标出现的预算被遵守：超时返回 timed_out 且一次 down 都不发。
//
// 为什么 fixture 用裸 Win32 而不是 Qt 控件：Qt 的无障碍桥把 UIA 包围盒上报成
// 逻辑像素（125% 下比物理像素小一圈），用它验收会把"坐标换算正确"误判成点歪了。
// 原生 BUTTON 由 Windows 自己的 provider 提供物理坐标，与真实目标窗口一致。
//
// 跑的时候不要动鼠标、不要锁屏。
#include "vision/visionservice.h"

#include <QApplication>
#include <QElapsedTimer>
#include <QEventLoop>
#include <QJsonArray>
#include <QJsonObject>
#include <QThread>

#include <cstdio>

#include <windows.h>

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

constexpr UINT_PTR kFirstButtonId = 100;
constexpr int kProbeCount = 5;
// 第 6 个按钮专门给 OCR：中文标签、大字号，OCR 才读得稳。
constexpr UINT_PTR kOcrButtonId = kFirstButtonId + kProbeCount;
int clicks[kProbeCount + 1] = {0, 0, 0, 0, 0, 0};
const wchar_t *const kLabels[kProbeCount] = {
    L"yiuk-unique-probe",     // 0：唯一标签
    L"yiuk-ambiguous-probe",  // 1：与 2 同标签、不同位置
    L"yiuk-ambiguous-probe",  // 2
    L"yiuk-order-probe",      // 3：只为占位，保证 2 的下方还有内容
    L"yiuk-order-probe",      // 4
};

LRESULT CALLBACK fixtureProc(HWND hwnd, UINT message, WPARAM wparam, LPARAM lparam) {
    if (message == WM_COMMAND && HIWORD(wparam) == BN_CLICKED) {
        const UINT_PTR id = LOWORD(wparam);
        if (id >= kFirstButtonId && id <= kOcrButtonId) ++clicks[id - kFirstButtonId];
        return 0;
    }
    if (message == WM_CLOSE) {
        DestroyWindow(hwnd);
        return 0;
    }
    return DefWindowProcW(hwnd, message, wparam, lparam);
}

// 把 fixture 拉到前台：UIA 只查前台窗口的控件树。
// 从后台进程启动时 Windows 会拒绝抢前台，这里用 AttachThreadInput 的常规做法切一次，
// 并回读 GetForegroundWindow 确认，不靠猜。
bool ensureForeground(HWND hwnd) {
    if (GetForegroundWindow() == hwnd) return true;
    const HWND previous = GetForegroundWindow();
    const DWORD previousThread = previous ? GetWindowThreadProcessId(previous, nullptr) : 0;
    const DWORD currentThread = GetCurrentThreadId();
    const bool attached = previousThread != 0 && previousThread != currentThread &&
                          AttachThreadInput(previousThread, currentThread, TRUE);
    SetForegroundWindow(hwnd);
    BringWindowToTop(hwnd);
    SetFocus(hwnd);
    if (attached) AttachThreadInput(previousThread, currentThread, FALSE);
    for (int i = 0; i < 20; ++i) {
        if (GetForegroundWindow() == hwnd) return true;
        QThread::msleep(50);
    }
    return false;
}

// 处理 Windows 消息：点击是投递到消息队列的，不泵就永远看不到计数变化。
void pumpFor(int milliseconds) {
    QElapsedTimer timer;
    timer.start();
    while (timer.elapsed() < milliseconds) {
        QCoreApplication::processEvents(QEventLoop::AllEvents, 10);
        QThread::msleep(2);
    }
}

QString clickTarget(yiuk::vision::VisionService *service, const QString &text, const QString &pick,
                    bool detectChange, QJsonObject *data, bool waitUntilFound = false, int waitTimeoutMs = 0) {
    yiuk::vision::ClickTargetOptions options;
    options.text.text = text;
    options.pick = pick;
    options.detectChange = detectChange;
    options.waitUntilFound = waitUntilFound;
    options.waitTimeoutMs = waitTimeoutMs;
    QString code;
    QString message;
    if (service->clickTarget(QStringLiteral("session-click-target"), options, nullptr, data, &code, &message))
        return QString();
    std::printf("     %s -> %s: %s\n", qPrintable(pick), qPrintable(code), qPrintable(message));
    return code;
}

int totalClicks() {
    int total = 0;
    for (int count : clicks) total += count;
    return total;
}

}  // namespace

int main(int argc, char **argv) {
    QApplication app(argc, argv);
    // 与真实客户端同名：截图/临时目录落在同一个 data\ 下，行为一致。
    QApplication::setApplicationName(QStringLiteral("YiukDeviceAssist"));
    QApplication::setOrganizationName(QStringLiteral("Yiuk"));

    WNDCLASSW windowClass{};
    windowClass.lpfnWndProc = fixtureProc;
    windowClass.hInstance = GetModuleHandleW(nullptr);
    windowClass.lpszClassName = L"yiuk-click-target-fixture";
    windowClass.hbrBackground = reinterpret_cast<HBRUSH>(COLOR_BTNFACE + 1);
    windowClass.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    if (RegisterClassW(&windowClass) == 0) {
        std::printf("SKIP cannot register the fixture window class\n");
        return 2;
    }
    // 放在屏幕中部：避开本机常驻的 MyDockFinder 等贴边浮层。
    HWND window = CreateWindowExW(0, windowClass.lpszClassName, L"yiuk-click-target-fixture",
                                 WS_OVERLAPPEDWINDOW | WS_VISIBLE, 760, 200, 420, 520, nullptr, nullptr,
                                 windowClass.hInstance, nullptr);
    if (window == nullptr) {
        std::printf("SKIP cannot create the fixture window\n");
        return 2;
    }
    for (int i = 0; i < kProbeCount; ++i) {
        CreateWindowExW(0, L"BUTTON", kLabels[i], WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_PUSHBUTTON, 30,
                        24 + i * 78, 340, 52, window, reinterpret_cast<HMENU>(kFirstButtonId + i),
                        windowClass.hInstance, nullptr);
    }
    // OCR 按钮：中文标签 + 22px 字体，避免小字被识别成一堆噪声。
    HWND ocrButton = CreateWindowExW(0, L"BUTTON", L"收藏 首页 设置", WS_CHILD | WS_VISIBLE | BS_PUSHBUTTON, 30,
                                     24 + kProbeCount * 78, 340, 52, window,
                                     reinterpret_cast<HMENU>(kOcrButtonId), windowClass.hInstance, nullptr);
    if (ocrButton != nullptr) {
        LOGFONTW labelFont{};
        labelFont.lfHeight = -22;
        lstrcpyW(labelFont.lfFaceName, L"Microsoft YaHei UI");
        HFONT font = CreateFontIndirectW(&labelFont);
        if (font != nullptr) SendMessageW(ocrButton, WM_SETFONT, reinterpret_cast<WPARAM>(font), TRUE);
    }
    pumpFor(300);
    if (!ensureForeground(window)) {
        std::printf("SKIP click_target needs an interactive desktop: the fixture cannot be made foreground\n");
        return 2;
    }
    pumpFor(400);  // 等一帧真正上屏，UIA 才枚举得到这些按钮

    yiuk::vision::VisionService service;
    QJsonObject data;
    QString code;

    // 1) 没有命中：一个 down 都不能发。
    data = QJsonObject();
    code = clickTarget(&service, QStringLiteral("yiuk-absent-probe-9f3a"), QStringLiteral("unique"), false, &data);
    pumpFor(150);
    check(code == QLatin1String("NOT_FOUND"), QStringLiteral("absent text returns NOT_FOUND"));
    check(totalClicks() == 0, QStringLiteral("NOT_FOUND does not send any mouse-down"));
    check(data.value(QStringLiteral("candidate_count")).toInt() == 0 &&
              data.value(QStringLiteral("matches")).isArray() &&
              data.value(QStringLiteral("screenshot_id")).toString().size() == 12,
          QStringLiteral("NOT_FOUND still carries the coordinate base and empty findings"));

    // 1b) 等目标出现：预算用完仍没命中 → NOT_FOUND + timed_out，一次 down 也不发。
    //     轮间会释放设备门，所以这段不会把设备锁住（另一个会话还能截屏）。
    QElapsedTimer waitTimer;
    waitTimer.start();
    data = QJsonObject();
    code = clickTarget(&service, QStringLiteral("yiuk-absent-probe-9f3a"), QStringLiteral("unique"), false, &data,
                       true, 1200);
    const qint64 waited = waitTimer.elapsed();
    pumpFor(120);
    check(code == QLatin1String("NOT_FOUND"), QStringLiteral("waiting for an absent target still ends in NOT_FOUND"));
    check(data.value(QStringLiteral("timed_out")).toBool(), QStringLiteral("waiting result is marked timed_out"));
    check(data.value(QStringLiteral("attempts")).toInt() >= 2,
          QStringLiteral("waiting re-searches the screen more than once"));
    check(waited >= 1000 && waited < 4000, QStringLiteral("waiting respects the timeout budget"));
    check(totalClicks() == 0, QStringLiteral("a wait timeout does not send any mouse-down"));

    // 1c) 等待并非“只会等”：目标已经在时，第一轮就命中，attempts=1、timed_out=false。
    //     （"目标稍后出现"这一段没法在本进程内造：UIA 只查前台窗口的控件树，
    //       而正在等待的线程就是这个窗口的主线程，不能同时新建控件。）
    data = QJsonObject();
    code = clickTarget(&service, QStringLiteral("yiuk-order-probe"), QStringLiteral("first"), false, &data, true,
                       3000);
    pumpFor(300);
    check(code.isEmpty() && data.value(QStringLiteral("attempts")).toInt() == 1 &&
              data.value(QStringLiteral("timed_out")).toBool() == false,
          QStringLiteral("waiting finds a present target on the first round"));
    check(clicks[3] + clicks[4] == 1, QStringLiteral("the first same-label candidate received the click"));

    // 2) 两个同标签按钮、位置不同、得分相同：判歧义，同样不点。
    if (!ensureForeground(window)) {
        std::printf("SKIP lost foreground before the ambiguity case\n");
        return 2;
    }
    data = QJsonObject();
    code = clickTarget(&service, QStringLiteral("yiuk-ambiguous-probe"), QStringLiteral("unique"), false, &data);
    pumpFor(150);
    check(code == QLatin1String("AMBIGUOUS_TARGET"),
          QStringLiteral("two same-label buttons are AMBIGUOUS_TARGET"));
    check(clicks[1] == 0 && clicks[2] == 0, QStringLiteral("AMBIGUOUS_TARGET does not send any mouse-down"));
    check(clicks[1] == 0 && clicks[2] == 0, QStringLiteral("neither same-label button was clicked"));
    check(data.value(QStringLiteral("candidate_count")).toInt() >= 2 &&
              data.value(QStringLiteral("top_score")).toDouble() > 0 &&
              data.value(QStringLiteral("competitor_score")).toDouble() > 0,
          QStringLiteral("ambiguity reports candidates and both scores"));

    // 3) 歧义时用 pick=index:2 指定第二个：只有它收到点击。
    if (!ensureForeground(window)) {
        std::printf("SKIP lost foreground before the index case\n");
        return 2;
    }
    data = QJsonObject();
    code = clickTarget(&service, QStringLiteral("yiuk-ambiguous-probe"), QStringLiteral("index:2"), true, &data);
    pumpFor(400);
    check(code.isEmpty(), QStringLiteral("pick=index:2 clicks the second candidate"));
    check(data.value(QStringLiteral("picked")).toObject().value(QStringLiteral("index")).toInt() == 2,
          QStringLiteral("result says which candidate was clicked"));
    check(clicks[2] == 1 && clicks[1] == 0 && clicks[0] == 0,
          QStringLiteral("the click landed on the second same-label button only"));
    check(data.value(QStringLiteral("screen_x")).toInt() > 0 &&
              data.value(QStringLiteral("changed_ratio")).isDouble(),
          QStringLiteral("result carries physical coordinates and change sampling"));

    // 4) 唯一命中：pick=best 点到那个按钮上。
    if (!ensureForeground(window)) {
        std::printf("SKIP lost foreground before the unique case\n");
        return 2;
    }
    data = QJsonObject();
    code = clickTarget(&service, QStringLiteral("yiuk-unique-probe"), QStringLiteral("best"), false, &data);
    pumpFor(400);
    check(code.isEmpty(), QStringLiteral("unique label is clicked with pick=best"));
    check(clicks[0] == 1, QStringLiteral("the unique button received the click"));
    check(data.value(QStringLiteral("candidate_count")).toInt() == 1,
          QStringLiteral("unique label reports a single candidate"));

    // 5) 落点必须是命中框中心：与 find_on_screen 的 center 同一算法。
    const QJsonObject picked = data.value(QStringLiteral("picked")).toObject();
    const QJsonArray center = picked.value(QStringLiteral("center")).toArray();
    const QJsonArray bbox = picked.value(QStringLiteral("bbox")).toArray();
    check(center.size() == 2 && data.value(QStringLiteral("x")).toInt() == center.at(0).toInt() &&
              data.value(QStringLiteral("y")).toInt() == center.at(1).toInt(),
          QStringLiteral("clicked point equals the candidate center"));
    if (center.size() == 2) {
        // 物理坐标下命中框中心应落在按钮矩形内：这是"点对了地方"的硬判据。
        const int x = data.value(QStringLiteral("screen_x")).toInt();
        const int y = data.value(QStringLiteral("screen_y")).toInt();
        check(bbox.size() == 4 && x >= bbox.at(0).toInt() && x <= bbox.at(2).toInt() && y >= bbox.at(1).toInt() &&
                  y <= bbox.at(3).toInt(),
              QStringLiteral("physical click point stays inside the picked bounding box"));
        std::printf("     picked bbox=[%d,%d,%d,%d] screen=%d,%d\n", bbox.at(0).toInt(), bbox.at(1).toInt(),
                    bbox.at(2).toInt(), bbox.at(3).toInt(), x, y);
    }

    // 6) OCR 兜底：显式 source=ocr，只在按钮附近的 region 里找中文标签。
    //    这一段验证的是「UIA 之外的来源」也能落到同一个点击事务里：
    //    region 用物理屏幕坐标，OCR 的框换回屏幕坐标后再过滤。
    if (!ensureForeground(window)) {
        std::printf("SKIP lost foreground before the OCR case\n");
        return 2;
    }
    if (ocrButton != nullptr) {
        RECT buttonRect{};
        GetWindowRect(ocrButton, &buttonRect);
        yiuk::vision::ClickTargetOptions ocrOptions;
        ocrOptions.text.text = QStringLiteral("收藏");
        ocrOptions.text.source = QStringLiteral("ocr");
        ocrOptions.text.region =
            QRect(buttonRect.left, buttonRect.top, buttonRect.right - buttonRect.left,
                  buttonRect.bottom - buttonRect.top)
                .adjusted(-4, -4, 4, 4);
        ocrOptions.pick = QStringLiteral("best");
        ocrOptions.detectChange = false;
        QJsonObject ocrData;
        QString ocrCode;
        QString ocrMessage;
        const bool ocrOk = service.clickTarget(QStringLiteral("session-click-target"), ocrOptions, nullptr,
                                               &ocrData, &ocrCode, &ocrMessage);
        pumpFor(400);
        if (!ocrOk && ocrCode == QLatin1String("OCR_UNAVAILABLE")) {
            std::printf("WARN OCR is unavailable on this machine: %s\n", qPrintable(ocrMessage));
        } else {
            check(ocrOk, QStringLiteral("source=ocr locates and clicks a CJK label"));
            if (ocrOk) {
                check(ocrData.value(QStringLiteral("source")).toString() == QStringLiteral("ocr"),
                      QStringLiteral("OCR result is tagged with source=ocr"));
                check(clicks[kProbeCount] == 1, QStringLiteral("the OCR-labelled button received the click"));
            }
        }
    }

    DestroyWindow(window);
    std::printf(failures == 0 ? "CLICKTARGET OK\n" : "CLICKTARGET FAILED (%d)\n", failures);
    return failures == 0 ? 0 : 1;
}
