#pragma once

#include <windows.h>

namespace yiuk {

// 管道由父客户端管理：父客户端启动此面板，并为每条消息向 stdin 写入一行 UTF-8 协议数据
// 读取器会将解码后的每一行发送给窗口，确保所有绘制都在 UI 线程中完成
constexpr UINT kMessageLine = WM_APP + 1;
constexpr UINT kMessageClosed = WM_APP + 2;

// 启动阻塞式读取线程
// 未继承 stdin 管道时返回 false，此时面板仅绘制 argv 传入的内容
bool StartStdinReader(HWND window);

}  // namespace yiuk
