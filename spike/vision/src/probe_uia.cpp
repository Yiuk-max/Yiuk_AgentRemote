// UIA 探针：直接在 MinGW 下用 COM 调 IUIAutomation，不经过 PowerShell。
// 回答三件事：能否编译链接、单次查询的真实耗时、粗暴全树遍历是否可控。

#define WIN32_LEAN_AND_MEAN
#include <windows.h>

#include <objbase.h>
#include <oleauto.h>
#include <uiautomation.h>

#include <chrono>
#include <cstdio>
#include <string>

namespace {

double nowMs() {
    using clock = std::chrono::steady_clock;
    static const clock::time_point start = clock::now();
    return std::chrono::duration<double, std::milli>(clock::now() - start).count();
}

std::string narrow(const wchar_t* text) {
    if (!text) return {};
    const int needed = WideCharToMultiByte(CP_UTF8, 0, text, -1, nullptr, 0, nullptr, nullptr);
    std::string out(size_t(needed > 0 ? needed - 1 : 0), '\0');
    if (needed > 1) WideCharToMultiByte(CP_UTF8, 0, text, -1, out.data(), needed, nullptr, nullptr);
    return out;
}

std::string jsonEscape(const std::string& value) {
    std::string out;
    for (char ch : value) {
        if (ch == '"' || ch == '\\') {
            out += '\\';
            out += ch;
        } else if (static_cast<unsigned char>(ch) < 0x20) {
            out += ' ';
        } else {
            out += ch;
        }
    }
    return out;
}

struct FindStat {
    long long count = 0;
    double ms = 0.0;
    HRESULT result = E_FAIL;
};

FindStat findAll(IUIAutomationElement* root, TreeScope scope, IUIAutomationCondition* condition) {
    FindStat stat;
    if (!root || !condition) return stat;
    IUIAutomationElementArray* array = nullptr;
    const double start = nowMs();
    stat.result = root->FindAll(scope, condition, &array);
    stat.ms = nowMs() - start;
    if (SUCCEEDED(stat.result) && array) {
        int length = 0;
        array->get_Length(&length);
        stat.count = length;
        array->Release();
    }
    return stat;
}

std::string elementName(IUIAutomationElement* element) {
    if (!element) return {};
    BSTR name = nullptr;
    if (FAILED(element->get_CurrentName(&name)) || !name) return {};
    const std::string out = narrow(name);
    SysFreeString(name);
    return out;
}

std::string elementClassName(IUIAutomationElement* element) {
    if (!element) return {};
    BSTR value = nullptr;
    if (FAILED(element->get_CurrentClassName(&value)) || !value) return {};
    const std::string out = narrow(value);
    SysFreeString(value);
    return out;
}

}  // namespace

int main() {
    const HRESULT comResult = CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    if (FAILED(comResult)) {
        std::printf("{\"ok\":false,\"error\":\"CoInitializeEx\",\"hr\":%ld}\n", long(comResult));
        return 1;
    }

    IUIAutomation* automation = nullptr;
    const double createStart = nowMs();
    const HRESULT createResult = CoCreateInstance(__uuidof(CUIAutomation), nullptr, CLSCTX_INPROC_SERVER,
                                                  __uuidof(IUIAutomation), reinterpret_cast<void**>(&automation));
    const double createMs = nowMs() - createStart;
    if (FAILED(createResult) || !automation) {
        std::printf("{\"ok\":false,\"error\":\"CoCreateInstance(CUIAutomation)\",\"hr\":%ld}\n", long(createResult));
        CoUninitialize();
        return 1;
    }

    const HWND foreground = GetForegroundWindow();
    wchar_t title[512] = {};
    if (foreground) GetWindowTextW(foreground, title, 512);

    IUIAutomationElement* root = nullptr;
    const double fromHandleStart = nowMs();
    const HRESULT fromHandle = automation->ElementFromHandle(foreground, &root);
    const double fromHandleMs = nowMs() - fromHandleStart;

    IUIAutomationCondition* trueCondition = nullptr;
    automation->CreateTrueCondition(&trueCondition);

    VARIANT buttonVariant;
    VariantInit(&buttonVariant);
    buttonVariant.vt = VT_I4;
    buttonVariant.lVal = UIA_ButtonControlTypeId;
    IUIAutomationCondition* buttonCondition = nullptr;
    automation->CreatePropertyCondition(UIA_ControlTypePropertyId, buttonVariant, &buttonCondition);

    const FindStat children = findAll(root, TreeScope_Children, trueCondition);
    const FindStat buttons = findAll(root, TreeScope_Descendants, buttonCondition);
    // 无过滤的全树遍历：这是最坏情况，耗时决定是否必须把 UIA 放到可回收的 helper 进程里。
    const FindStat descendants = findAll(root, TreeScope_Descendants, trueCondition);

    POINT cursor{};
    GetCursorPos(&cursor);
    IUIAutomationElement* underPoint = nullptr;
    const double fromPointStart = nowMs();
    const HRESULT fromPoint = automation->ElementFromPoint(cursor, &underPoint);
    const double fromPointMs = nowMs() - fromPointStart;

    const double perNode = descendants.count > 0 ? descendants.ms / double(descendants.count) : 0.0;
    const double worst = children.ms > buttons.ms ? (children.ms > descendants.ms ? children.ms : descendants.ms)
                                                  : (buttons.ms > descendants.ms ? buttons.ms : descendants.ms);

    std::printf(
        "{\"ok\":%s,\"create_ms\":%.2f,\"from_handle_ms\":%.2f,\"foreground\":{\"title\":\"%s\","
        "\"name\":\"%s\",\"class\":\"%s\"},\"app_children_true\":{\"count\":%lld,\"ms\":%.2f},"
        "\"descendants_buttons\":{\"count\":%lld,\"ms\":%.2f},\"descendants_true\":{\"count\":%lld,\"ms\":%.2f},"
        "\"per_node_ms\":%.4f,\"worst_single_query_ms\":%.2f,\"from_point_ms\":%.2f,\"from_point_name\":\"%s\"}\n",
        SUCCEEDED(fromHandle) && root ? "true" : "false", createMs, fromHandleMs, jsonEscape(narrow(title)).c_str(),
        jsonEscape(elementName(root)).c_str(), jsonEscape(elementClassName(root)).c_str(), children.count,
        children.ms, buttons.count, buttons.ms, descendants.count, descendants.ms, perNode, worst, fromPointMs,
        jsonEscape(elementName(underPoint)).c_str());

    if (underPoint) underPoint->Release();
    if (root) root->Release();
    if (trueCondition) trueCondition->Release();
    if (buttonCondition) buttonCondition->Release();
    automation->Release();
    CoUninitialize();
    return 0;
}
