#pragma once

#include <QString>

namespace yiuk::vision {

// 程序自己的数据根目录：**exe 所在目录下的 `data\`**（绿色版：所有文件都跟着程序走）。
// 目录不可写（例如装在 Program Files）时回退到 %LOCALAPPDATA%\Yiuk\DeviceAssist，
// 保证不会因为只读目录而丢掉会话/截图。
QString programDataRoot();

// 数据根下的子路径，自动创建目录；relative 为空时返回根本身。
QString programDataPath(const QString &relative);

// 程序目录是否真的可写（用于日志/诊断）。
bool programDataIsPortable();

}  // namespace yiuk::vision
