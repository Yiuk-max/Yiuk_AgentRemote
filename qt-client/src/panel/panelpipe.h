#pragma once

#include <windows.h>

namespace yiuk {

// The parent client owns the pipe: it launches this panel and writes one UTF-8
// protocol line per message to our stdin. The reader posts every decoded line to
// the window so all rendering stays on the UI thread.
constexpr UINT kMessageLine = WM_APP + 1;
constexpr UINT kMessageClosed = WM_APP + 2;

// Starts the blocking reader thread. Returns false when no stdin pipe was
// inherited, in which case the panel simply renders what argv gave it.
bool StartStdinReader(HWND window);

}  // namespace yiuk
