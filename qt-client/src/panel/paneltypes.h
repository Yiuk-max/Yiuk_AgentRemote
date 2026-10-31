#pragma once

#include <string>

namespace yiuk {

// 远程 Agent 在此设备上执行的每种操作都有对应类型
// 面板目前只用文字颜色区分类型（后续再支持 pi 的背景色块），因此类型决定边栏标签、强调色条和条目文字颜色
enum class Kind {
    Info,
    Session,
    Execute,
    Output,
    Error,
    Read,
    Download,
    Upload,
    Success,
};

struct Entry {
    Kind kind = Kind::Info;
    std::wstring time;  // "HH:MM:SS"，由父进程预先格式化
    std::wstring text;  // 可能包含 '\n'；面板会自行换行
};

// 父进程发送状态标记而非颜色："active" 对应绿色圆点，"pending" 对应琥珀色圆点，"closed" 和 "none" 使用弱化颜色
enum class SessionState {
    None,
    Active,
    Pending,
    Closed,
};

struct PanelParams {
    std::wstring title = L"远程协助活动";
    std::wstring label;   // "Agent · 453 313 764"
    std::wstring status;  // "远程协助进行中"
    SessionState state = SessionState::None;
};

}  // namespace yiuk
