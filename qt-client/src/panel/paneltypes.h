#pragma once

#include <string>

namespace yiuk {

// Every operation a remote Agent performs on this device has its own kind. The
// panel renders text colours only for now (pi's background boxes are a later
// step), so the kind decides the gutter label, the accent bar and the ink of the
// entry.
enum class Kind {
    Info,
    Session,
    Execute,
    Output,
    Error,
    Read,
    Download,
    Success,
};

struct Entry {
    Kind kind = Kind::Info;
    std::wstring time;  // "HH:MM:SS", already formatted by the parent
    std::wstring text;  // may contain '\n'; the panel wraps it itself
};

// The parent sends a state token, never a colour: "active" drives the green dot,
// "pending" the amber one, "closed" and "none" stay muted.
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
