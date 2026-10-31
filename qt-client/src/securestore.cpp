#include "securestore.h"

#include <QByteArray>

#ifdef Q_OS_WIN
#include <windows.h>
#include <wincrypt.h>
#endif

namespace {
const QLatin1String kDpapiPrefix("dpapi:");
const QLatin1String kPlainPrefix("plain:");
} // namespace

QString SecureStore::protect(const QString &plain) {
    if (plain.isEmpty()) return QString();
#ifdef Q_OS_WIN
    const QByteArray utf8 = plain.toUtf8();
    DATA_BLOB input;
    input.pbData = reinterpret_cast<BYTE *>(const_cast<char *>(utf8.constData()));
    input.cbData = static_cast<DWORD>(utf8.size());
    DATA_BLOB output;
    output.pbData = nullptr;
    output.cbData = 0;
    if (CryptProtectData(&input, L"Yiuk Device Assist", nullptr, nullptr, nullptr, 0, &output)) {
        const QByteArray blob(reinterpret_cast<const char *>(output.pbData), static_cast<int>(output.cbData));
        LocalFree(output.pbData);
        return kDpapiPrefix + QString::fromLatin1(blob.toBase64());
    }
#endif
    return kPlainPrefix + QString::fromLatin1(plain.toUtf8().toBase64());
}

QString SecureStore::unprotect(const QString &stored) {
    if (stored.isEmpty()) return QString();
    if (stored.startsWith(kDpapiPrefix)) {
#ifdef Q_OS_WIN
        const QByteArray blob = QByteArray::fromBase64(stored.mid(kDpapiPrefix.size()).toLatin1());
        DATA_BLOB input;
        input.pbData = reinterpret_cast<BYTE *>(const_cast<char *>(blob.constData()));
        input.cbData = static_cast<DWORD>(blob.size());
        DATA_BLOB output;
        output.pbData = nullptr;
        output.cbData = 0;
        if (CryptUnprotectData(&input, nullptr, nullptr, nullptr, nullptr, 0, &output)) {
            const QString plain = QString::fromUtf8(reinterpret_cast<const char *>(output.pbData), static_cast<int>(output.cbData));
            LocalFree(output.pbData);
            return plain;
        }
        return QString();
#else
        return QString();
#endif
    }
    if (stored.startsWith(kPlainPrefix))
        return QString::fromUtf8(QByteArray::fromBase64(stored.mid(kPlainPrefix.size()).toLatin1()));
    // 为兼容旧数据，将没有前缀的值视为明文
    return stored;
}