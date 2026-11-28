#include "textlocator.h"

#include <QElapsedTimer>

#include <algorithm>

#define WIN32_LEAN_AND_MEAN
#include <windows.h>

#include <objbase.h>
#include <oleauto.h>
#include <uiautomation.h>

namespace yiuk::vision {

namespace {

// 每个 worker 线程自己 CoInitializeEx，离开作用域就 CoUninitialize；
// UIA 用 MTA 就够（探针里也是这个用法），不需要消息泵。
//
// 注意 RPC_E_CHANGED_MODE：调用线程已经用别的 apartment 初始化过 COM（典型是 Qt 主线程
// 被 OleInitialize 拉成 STA）。这种情况下 CoInitializeEx 失败但 COM **是**可用的，
// UIA 在 STA 线程里照样能当客户端用；把它当“COM 不可用”会让 find/click_target
// 在主线程里直接降级成 UIA_UNAVAILABLE。只在自己初始化成功时才配对 Uninitialize。
struct ComScope {
    HRESULT result = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    bool owns = SUCCEEDED(result);
    bool usable() const { return owns || result == RPC_E_CHANGED_MODE; }
    ~ComScope() {
        if (owns) CoUninitialize();
    }
};

QString takeBstr(BSTR value) {
    if (value == nullptr) return QString();
    const QString text = QString::fromWCharArray(value, static_cast<int>(SysStringLen(value)));
    SysFreeString(value);
    return text;
}

const QHash<int, QString> &controlTypeTable() {
    static const QHash<int, QString> table{
        {static_cast<int>(UIA_ButtonControlTypeId), QStringLiteral("Button")},
        {static_cast<int>(UIA_CalendarControlTypeId), QStringLiteral("Calendar")},
        {static_cast<int>(UIA_CheckBoxControlTypeId), QStringLiteral("CheckBox")},
        {static_cast<int>(UIA_ComboBoxControlTypeId), QStringLiteral("ComboBox")},
        {static_cast<int>(UIA_EditControlTypeId), QStringLiteral("Edit")},
        {static_cast<int>(UIA_HyperlinkControlTypeId), QStringLiteral("Hyperlink")},
        {static_cast<int>(UIA_ImageControlTypeId), QStringLiteral("Image")},
        {static_cast<int>(UIA_ListItemControlTypeId), QStringLiteral("ListItem")},
        {static_cast<int>(UIA_ListControlTypeId), QStringLiteral("List")},
        {static_cast<int>(UIA_MenuBarControlTypeId), QStringLiteral("MenuBar")},
        {static_cast<int>(UIA_MenuItemControlTypeId), QStringLiteral("MenuItem")},
        {static_cast<int>(UIA_MenuControlTypeId), QStringLiteral("Menu")},
        {static_cast<int>(UIA_ProgressBarControlTypeId), QStringLiteral("ProgressBar")},
        {static_cast<int>(UIA_RadioButtonControlTypeId), QStringLiteral("RadioButton")},
        {static_cast<int>(UIA_ScrollBarControlTypeId), QStringLiteral("ScrollBar")},
        {static_cast<int>(UIA_SliderControlTypeId), QStringLiteral("Slider")},
        {static_cast<int>(UIA_SpinnerControlTypeId), QStringLiteral("Spinner")},
        {static_cast<int>(UIA_StatusBarControlTypeId), QStringLiteral("StatusBar")},
        {static_cast<int>(UIA_TabItemControlTypeId), QStringLiteral("TabItem")},
        {static_cast<int>(UIA_TabControlTypeId), QStringLiteral("Tab")},
        {static_cast<int>(UIA_TextControlTypeId), QStringLiteral("Text")},
        {static_cast<int>(UIA_ToolBarControlTypeId), QStringLiteral("ToolBar")},
        {static_cast<int>(UIA_ToolTipControlTypeId), QStringLiteral("ToolTip")},
        {static_cast<int>(UIA_TreeItemControlTypeId), QStringLiteral("TreeItem")},
        {static_cast<int>(UIA_TreeControlTypeId), QStringLiteral("Tree")},
        {static_cast<int>(UIA_CustomControlTypeId), QStringLiteral("Custom")},
        {static_cast<int>(UIA_GroupControlTypeId), QStringLiteral("Group")},
        {static_cast<int>(UIA_ThumbControlTypeId), QStringLiteral("Thumb")},
        {static_cast<int>(UIA_DataGridControlTypeId), QStringLiteral("DataGrid")},
        {static_cast<int>(UIA_DataItemControlTypeId), QStringLiteral("DataItem")},
        {static_cast<int>(UIA_DocumentControlTypeId), QStringLiteral("Document")},
        {static_cast<int>(UIA_SplitButtonControlTypeId), QStringLiteral("SplitButton")},
        {static_cast<int>(UIA_WindowControlTypeId), QStringLiteral("Window")},
        {static_cast<int>(UIA_PaneControlTypeId), QStringLiteral("Pane")},
        {static_cast<int>(UIA_HeaderControlTypeId), QStringLiteral("Header")},
        {static_cast<int>(UIA_HeaderItemControlTypeId), QStringLiteral("HeaderItem")},
        {static_cast<int>(UIA_TableControlTypeId), QStringLiteral("Table")},
        {static_cast<int>(UIA_TitleBarControlTypeId), QStringLiteral("TitleBar")},
        {static_cast<int>(UIA_SeparatorControlTypeId), QStringLiteral("Separator")},
    };
    return table;
}

QString foregroundTitle() {
    const HWND window = GetForegroundWindow();
    if (window == nullptr) return QString();
    wchar_t buffer[512] = {};
    const int length = GetWindowTextW(window, buffer, static_cast<int>(sizeof(buffer) / sizeof(buffer[0])));
    return length > 0 ? QString::fromWCharArray(buffer, length) : QString();
}

}  // namespace

QString normalizeText(const QString &value) {
    QString out;
    out.reserve(value.size());
    for (const QChar ch : value) {
        const ushort code = ch.unicode();
        // 全角 ASCII（！..～）与全角空格 -> 半角；CJK 字符本身不动。
        if (code >= 0xFF01 && code <= 0xFF5E) {
            out += QChar(static_cast<ushort>(code - 0xFEE0));
            continue;
        }
        if (code == 0x3000) continue;         // 全角空格
        if (ch.isSpace()) continue;           // 去空白：OCR 的"收 藏"与"收藏"要能匹配
        out += ch;
    }
    return out.toCaseFolded();
}

QString controlTypeName(int controlTypeId) {
    return controlTypeTable().value(controlTypeId);
}

int controlTypeId(const QString &name) {
    if (name.isEmpty()) return 0;
    const QString wanted = name.trimmed().toLower();
    for (auto it = controlTypeTable().constBegin(); it != controlTypeTable().constEnd(); ++it) {
        if (it.value().toLower() == wanted) return it.key();
    }
    return 0;
}

bool findText(const TextQuery &query, TextSearchOutcome *outcome, QString *code, QString *message) {
    auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (outcome == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("outcome 为空"));
    *outcome = TextSearchOutcome{};

    const QString needle = normalizeText(query.text);
    if (needle.isEmpty())
        return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("text 不能为空（归一化后没有内容）"));
    const bool exact = query.match.compare(QLatin1String("exact"), Qt::CaseInsensitive) == 0;
    const int wantedControlType = controlTypeId(query.controlType);
    if (!query.controlType.isEmpty() && wantedControlType == 0)
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("未知的 control_type：%1").arg(query.controlType));

    ComScope com;
    if (!com.usable())
        return fail(QStringLiteral("UIA_UNAVAILABLE"),
                    QStringLiteral("UIA 初始化失败（COM 不可用），可能处于受限会话"));

    IUIAutomation *automation = nullptr;
    const HRESULT created = CoCreateInstance(__uuidof(CUIAutomation), nullptr, CLSCTX_INPROC_SERVER,
                                             __uuidof(IUIAutomation), reinterpret_cast<void **>(&automation));
    if (FAILED(created) || automation == nullptr)
        return fail(QStringLiteral("UIA_UNAVAILABLE"), QStringLiteral("无法创建 UIA 实例"));

    auto releaseAutomation = [&automation]() {
        if (automation) {
            automation->Release();
            automation = nullptr;
        }
    };

    const HWND foreground = GetForegroundWindow();
    if (foreground == nullptr) {
        releaseAutomation();
        return fail(QStringLiteral("UIA_UNAVAILABLE"), QStringLiteral("没有前台窗口（桌面可能已锁定）"));
    }
    const QString windowTitle = foregroundTitle();

    IUIAutomationElement *root = nullptr;
    if (FAILED(automation->ElementFromHandle(foreground, &root)) || root == nullptr) {
        releaseAutomation();
        return fail(QStringLiteral("UIA_UNAVAILABLE"),
                    QStringLiteral("无法读取前台窗口 %1 的控件树").arg(windowTitle));
    }

    // 控制视图：比原始树小得多（P0 实测 Edge 746 节点），仍足够覆盖常见控件。
    IUIAutomationCondition *condition = nullptr;
    if (FAILED(automation->get_ControlViewCondition(&condition)) || condition == nullptr) {
        root->Release();
        releaseAutomation();
        return fail(QStringLiteral("UIA_UNAVAILABLE"), QStringLiteral("无法获取 UIA 控制视图条件"));
    }

    QElapsedTimer timer;
    timer.start();
    IUIAutomationElementArray *array = nullptr;
    const HRESULT found = root->FindAll(TreeScope_Descendants, condition, &array);
    condition->Release();
    root->Release();
    if (FAILED(found) || array == nullptr) {
        releaseAutomation();
        return fail(QStringLiteral("UIA_UNAVAILABLE"), QStringLiteral("UIA 遍历前台窗口失败"));
    }

    int length = 0;
    array->get_Length(&length);
    const int budgetNodes = std::max(1, query.maxNodes);
    const int budgetMs = std::max(1, query.timeBudgetMs);
    const int limit = std::max(1, query.limit);

    QVector<TextMatch> hits;
    bool truncated = false;
    int visited = 0;
    for (int i = 0; i < length; ++i) {
        if (visited >= budgetNodes || timer.elapsed() > budgetMs) {
            truncated = true;
            break;
        }
        IUIAutomationElement *element = nullptr;
        if (FAILED(array->GetElement(i, &element)) || element == nullptr) continue;
        ++visited;

        BOOL offscreen = FALSE;
        element->get_CurrentIsOffscreen(&offscreen);
        if (offscreen) {
            element->Release();
            continue;
        }

        BSTR name = nullptr;
        element->get_CurrentName(&name);
        const QString elementName = takeBstr(name);
        if (elementName.isEmpty()) {
            element->Release();
            continue;
        }
        const QString normalized = normalizeText(elementName);
        const bool matched = exact ? normalized == needle : normalized.contains(needle);
        if (!matched) {
            element->Release();
            continue;
        }

        CONTROLTYPEID typeId = 0;
        element->get_CurrentControlType(&typeId);
        if (wantedControlType != 0 && static_cast<int>(typeId) != wantedControlType) {
            element->Release();
            continue;
        }

        RECT rect{};
        element->get_CurrentBoundingRectangle(&rect);
        element->Release();
        const QRect bounds(rect.left, rect.top, rect.right - rect.left, rect.bottom - rect.top);
        if (bounds.width() <= 0 || bounds.height() <= 0) continue;
        if (!query.region.isNull() && !query.region.intersects(bounds)) continue;

        TextMatch hit;
        hit.rect = bounds;
        hit.name = elementName;
        hit.controlType = controlTypeName(static_cast<int>(typeId));
        // 文本匹配质量：完全相同 1.0，包含但长度不同 0.9（不伪装成图像置信度）。
        hit.score = normalized == needle ? 1.0 : 0.9;
        hits.append(hit);
    }
    array->Release();
    releaseAutomation();

    // 阅读顺序：先上后下、再左到右；截断前必须先把歧义判完。
    std::stable_sort(hits.begin(), hits.end(), [](const TextMatch &a, const TextMatch &b) {
        if (a.rect.top() != b.rect.top()) return a.rect.top() < b.rect.top();
        return a.rect.left() < b.rect.left();
    });
    if (hits.size() > limit) {
        hits.resize(limit);
        truncated = true;
    }

    outcome->matches = hits;
    outcome->truncated = truncated;
    outcome->visitedNodes = visited;
    outcome->elapsedMs = static_cast<int>(timer.elapsed());
    return true;
}

}  // namespace yiuk::vision
