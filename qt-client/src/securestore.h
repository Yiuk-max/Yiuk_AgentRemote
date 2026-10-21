#pragma once

#include <QString>

// SecureStore protects small secrets (the platform player password) at rest.
// On Windows it uses DPAPI so only the current Windows user can decrypt the
// value. On other platforms it falls back to an explicit plaintext marker so
// development builds still work without silently pretending to be secure.
namespace SecureStore {

QString protect(const QString &plain);
QString unprotect(const QString &stored);

} // namespace SecureStore