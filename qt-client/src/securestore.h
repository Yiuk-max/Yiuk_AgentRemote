#pragma once

#include <QString>

// SecureStore 用于保护静态存储的小型机密信息（平台玩家密码）
// Windows 上使用 DPAPI，只有当前 Windows 用户能够解密
// 其他平台则使用明确的明文标记，确保开发构建仍可使用，同时不会伪装成安全存储
namespace SecureStore {

QString protect(const QString &plain);
QString unprotect(const QString &stored);

} // namespace SecureStore