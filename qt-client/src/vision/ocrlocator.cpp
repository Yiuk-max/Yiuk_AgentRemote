#include "ocrlocator.h"

#include <QByteArray>
#include <QElapsedTimer>
#include <QThread>

#include <algorithm>
#include <cmath>

// MinGW：这些 WinRT 头可以编译（probe_winrt_ocr 已验证），但 winrt/*.h 不行。
// WIN32_LEAN_AND_MEAN / NOMINMAX 已由 target_compile_definitions 给出，这里只兜底。
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>

#include <hstring.h>
#include <inspectable.h>
#include <roapi.h>
#include <winstring.h>

namespace yiuk::vision {

namespace {

// ---- WinRT ABI：顺序即 vtable 顺序，IID 与 SDK 10.0.26100 一致 ----

MIDL_INTERFACE("ea79a752-f7c2-4265-b1bd-c4dec4e4f080")
ILanguageAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_LanguageTag(HSTRING *value) = 0;
};

MIDL_INTERFACE("905a0fef-bc53-11df-8c49-001e4fc686da")
IBufferByteAccess : public IUnknown {
   public:
    virtual HRESULT STDMETHODCALLTYPE Buffer(byte **value) = 0;
};

MIDL_INTERFACE("71af914d-c10f-484b-bc50-14bc623b3a27")
IBufferFactory : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE Create(UINT32 capacity, IInspectable **value) = 0;
};

// IVectorView<T>：只用到 GetAt 与 get_Size，声明到 get_Size 为止。
template <class T>
struct IVectorViewAbi : IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE GetAt(UINT32 index, T **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Size(UINT32 *value) = 0;
};

// IAsyncOperation<T>：put_Completed / get_Completed / GetResults。
// 状态轮询走 IAsyncInfo，所以这里不实现 Completed 回调对象。
template <class T>
struct IAsyncOperationAbi : IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE put_Completed(IInspectable *handler) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Completed(IInspectable **handler) = 0;
    virtual HRESULT STDMETHODCALLTYPE GetResults(T **results) = 0;
};

MIDL_INTERFACE("00000036-0000-0000-C000-000000000046")
IAsyncInfoAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_Id(UINT32 *value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Status(int *value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_ErrorCode(HRESULT *value) = 0;
    virtual HRESULT STDMETHODCALLTYPE Cancel() = 0;
    virtual HRESULT STDMETHODCALLTYPE Close() = 0;
};

struct AbiRect {
    float X;
    float Y;
    float Width;
    float Height;
};

MIDL_INTERFACE("3c2a477a-5cd9-3525-ba2a-23d1e0a68a1d")
IOcrWordAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_BoundingRect(AbiRect *value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Text(HSTRING *value) = 0;
};

MIDL_INTERFACE("0043a16f-e31f-3a24-899c-d444bd088124")
IOcrLineAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_Words(IVectorViewAbi<IOcrWordAbi> **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Text(HSTRING *value) = 0;
};

MIDL_INTERFACE("9bd235b2-175b-3d6a-92e2-388c206e2f63")
IOcrResultAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_Lines(IVectorViewAbi<IOcrLineAbi> **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_TextAngle(IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Text(HSTRING *value) = 0;
};

MIDL_INTERFACE("5a14bc41-5b76-3140-b680-8825562683ac")
IOcrEngineAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE RecognizeAsync(IInspectable *bitmap,
                                                     IAsyncOperationAbi<IOcrResultAbi> **result) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_RecognizerLanguage(ILanguageAbi **value) = 0;
};

MIDL_INTERFACE("5bffa85a-3384-3540-9940-699120d428a8")
IOcrEngineStaticsAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_MaxImageDimension(UINT32 *value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_AvailableRecognizerLanguages(IVectorViewAbi<ILanguageAbi> **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE IsLanguageSupported(ILanguageAbi *language, boolean *value) = 0;
    virtual HRESULT STDMETHODCALLTYPE TryCreateFromLanguage(ILanguageAbi *language, IOcrEngineAbi **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE TryCreateFromUserProfileLanguages(IOcrEngineAbi **value) = 0;
};

// ISoftwareBitmapStatics：只用到 CreateCopyFromBuffer（第 4 个自有方法）。
MIDL_INTERFACE("df0385db-672f-4a9d-806e-c2442f343e86")
ISoftwareBitmapStaticsAbi : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE Copy(IInspectable *source, IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE Convert(IInspectable *source, int format, IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE ConvertWithAlpha(IInspectable *source, int format, int alpha,
                                                       IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyFromBuffer(IInspectable *source, int format, INT32 width,
                                                           INT32 height, IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyWithAlphaFromBuffer(IInspectable *source, int format, INT32 width,
                                                                    INT32 height, int alpha,
                                                                    IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyFromSurfaceAsync(IInspectable *surface, IInspectable **value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyWithAlphaFromSurfaceAsync(IInspectable *surface, int alpha,
                                                                          IInspectable **value) = 0;
};

const GUID kIID_OcrEngineStatics = {0x5bffa85a, 0x3384, 0x3540, {0x99, 0x40, 0x69, 0x91, 0x20, 0xd4, 0x28, 0xa8}};
const GUID kIID_BufferFactory = {0x71af914d, 0xc10f, 0x484b, {0xbc, 0x50, 0x14, 0xbc, 0x62, 0x3b, 0x3a, 0x27}};
const GUID kIID_SoftwareBitmapStatics = {0xdf0385db, 0x672f, 0x4a9d,
                                         {0x80, 0x6e, 0xc2, 0x44, 0x2f, 0x34, 0x3e, 0x86}};
const GUID kIID_AsyncInfo = {0x00000036, 0x0000, 0x0000, {0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}};
const GUID kIID_BufferByteAccess = {0x905a0fef, 0xbc53, 0x11df,
                                    {0x8c, 0x49, 0x00, 0x1e, 0x4f, 0xc6, 0x86, 0xda}};

// BitmapPixelFormat_Bgra8 = 87；IAsyncInfo 状态：0 = Started，1 = Completed。
constexpr int kBitmapPixelFormatBgra8 = 87;
constexpr int kAsyncStarted = 0;
constexpr int kAsyncCompleted = 1;
// 单次识别的硬上限：正常 2560x1600 全屏 200–400ms，超过这个就判定为卡住而不是慢。
constexpr int kRecognizeTimeoutMs = 8000;

struct RoScope {
    HRESULT result = RoInitialize(RO_INIT_MULTITHREADED);
    // 线程已经用别的 apartment 初始化过 COM 时 RoInitialize 会失败，但 WinRT 仍可用；
    // 只在自己初始化成功时才配对 RoUninitialize。
    bool owns = SUCCEEDED(result);
    bool usable() const { return owns || result == RPC_E_CHANGED_MODE; }
    ~RoScope() {
        if (owns) RoUninitialize();
    }
};

template <typename T>
void release(T *&pointer) {
    if (pointer) {
        pointer->Release();
        pointer = nullptr;
    }
}

HRESULT activate(const wchar_t *className, REFIID iid, void **out) {
    HSTRING name = nullptr;
    HRESULT hr = WindowsCreateString(className, UINT32(wcslen(className)), &name);
    if (FAILED(hr)) return hr;
    hr = RoGetActivationFactory(name, iid, out);
    WindowsDeleteString(name);
    return hr;
}

QString hstringToQString(HSTRING value) {
    if (value == nullptr) return QString();
    UINT32 length = 0;
    const wchar_t *raw = WindowsGetStringRawBuffer(value, &length);
    if (raw == nullptr || length == 0) return QString();
    return QString::fromWCharArray(raw, static_cast<int>(length));
}

bool containsCjk(const QString &text) {
    for (const QChar ch : text) {
        const ushort value = ch.unicode();
        if ((value >= 0x3040 && value <= 0x30FF) ||    // 日文假名
            (value >= 0x3400 && value <= 0x9FFF) ||    // CJK 统一表意
            (value >= 0xAC00 && value <= 0xD7AF) ||    // 韩文
            (value >= 0xF900 && value <= 0xFAFF))      // CJK 兼容表意
            return true;
    }
    return false;
}

// 走 vtable 取 IBuffer::put_Length（IBuffer 的第 3 个自有方法，索引 8）。
// Create() 只设置 Capacity，而 CreateCopyFromBuffer 依据 Length 取数据。
HRESULT putBufferLength(IInspectable *buffer, UINT32 length) {
    void **vtable = *reinterpret_cast<void ***>(buffer);
    using PutLengthFn = HRESULT(STDMETHODCALLTYPE *)(void *, UINT32);
    const auto putLength = reinterpret_cast<PutLengthFn>(vtable[8]);
    return putLength(buffer, length);
}

// 把 QImage 包装成 SoftwareBitmap(Bgra8)。失败时写 error。
IInspectable *softwareBitmapFrom(const QImage &image, QString *error) {
    const QImage frame = image.convertToFormat(QImage::Format_ARGB32);
    if (frame.isNull()) {
        *error = QStringLiteral("图像转换失败");
        return nullptr;
    }
    const int width = frame.width();
    const int height = frame.height();
    const int rowBytes = width * 4;
    // SoftwareBitmap 要连续内存：QImage 每行可能带对齐填充，逐行拷过去。
    QByteArray pixels(static_cast<int>(qsizetype(rowBytes) * height), '\0');
    for (int y = 0; y < height; ++y)
        memcpy(pixels.data() + qsizetype(y) * rowBytes, frame.constScanLine(y), size_t(rowBytes));

    IBufferFactory *bufferFactory = nullptr;
    HRESULT hr = activate(L"Windows.Storage.Streams.Buffer", kIID_BufferFactory,
                          reinterpret_cast<void **>(&bufferFactory));
    if (FAILED(hr) || bufferFactory == nullptr) {
        *error = QStringLiteral("Buffer 激活失败（hr=%1）").arg(qlonglong(hr));
        return nullptr;
    }
    IInspectable *buffer = nullptr;
    hr = bufferFactory->Create(UINT32(pixels.size()), &buffer);
    release(bufferFactory);
    if (FAILED(hr) || buffer == nullptr) {
        *error = QStringLiteral("IBuffer 创建失败（hr=%1）").arg(qlonglong(hr));
        return nullptr;
    }

    IBufferByteAccess *byteAccess = nullptr;
    hr = buffer->QueryInterface(kIID_BufferByteAccess, reinterpret_cast<void **>(&byteAccess));
    if (FAILED(hr) || byteAccess == nullptr) {
        *error = QStringLiteral("IBufferByteAccess 不可用（hr=%1）").arg(qlonglong(hr));
        release(buffer);
        return nullptr;
    }
    byte *bytes = nullptr;
    hr = byteAccess->Buffer(&bytes);
    if (FAILED(hr) || bytes == nullptr) {
        *error = QStringLiteral("IBuffer 数据指针不可用（hr=%1）").arg(qlonglong(hr));
        release(byteAccess);
        release(buffer);
        return nullptr;
    }
    memcpy(bytes, pixels.constData(), size_t(pixels.size()));
    release(byteAccess);

    hr = putBufferLength(buffer, UINT32(pixels.size()));
    if (FAILED(hr)) {
        *error = QStringLiteral("IBuffer::put_Length 失败（hr=%1）").arg(qlonglong(hr));
        release(buffer);
        return nullptr;
    }

    ISoftwareBitmapStaticsAbi *bitmapStatics = nullptr;
    hr = activate(L"Windows.Graphics.Imaging.SoftwareBitmap", kIID_SoftwareBitmapStatics,
                  reinterpret_cast<void **>(&bitmapStatics));
    if (FAILED(hr) || bitmapStatics == nullptr) {
        *error = QStringLiteral("SoftwareBitmap 激活失败（hr=%1）").arg(qlonglong(hr));
        release(buffer);
        return nullptr;
    }
    IInspectable *bitmap = nullptr;
    hr = bitmapStatics->CreateCopyFromBuffer(buffer, kBitmapPixelFormatBgra8, width, height, &bitmap);
    release(bitmapStatics);
    release(buffer);
    if (FAILED(hr) || bitmap == nullptr) {
        *error = QStringLiteral("CreateCopyFromBuffer 失败（hr=%1）").arg(qlonglong(hr));
        return nullptr;
    }
    return bitmap;
}

// 按查询文本挑识别语言：含 CJK 时优先中文/日文/韩文引擎，否则用用户配置。
IOcrEngineAbi *createEngine(IOcrEngineStaticsAbi *statics, const QString &text, QString *language,
                            QString *error) {
    const bool wantsCjk = containsCjk(text);
    QStringList tags;
    IOcrEngineAbi *engine = nullptr;

    IVectorViewAbi<ILanguageAbi> *available = nullptr;
    if (SUCCEEDED(statics->get_AvailableRecognizerLanguages(&available)) && available != nullptr) {
        UINT32 size = 0;
        available->get_Size(&size);
        for (UINT32 i = 0; i < size; ++i) {
            ILanguageAbi *item = nullptr;
            if (FAILED(available->GetAt(i, &item)) || item == nullptr) continue;
            HSTRING tag = nullptr;
            QString tagText;
            if (SUCCEEDED(item->get_LanguageTag(&tag)) && tag != nullptr) {
                tagText = hstringToQString(tag);
                WindowsDeleteString(tag);
            }
            tags.append(tagText);
            const bool preferThis =
                wantsCjk ? (tagText.startsWith(QLatin1String("zh")) || tagText.startsWith(QLatin1String("ja")) ||
                            tagText.startsWith(QLatin1String("ko")))
                         : engine == nullptr;
            if (engine == nullptr && preferThis &&
                SUCCEEDED(statics->TryCreateFromLanguage(item, &engine)) && engine != nullptr) {
                *language = tagText;
            }
            release(item);
        }
        release(available);
    }

    if (engine == nullptr && !wantsCjk && SUCCEEDED(statics->TryCreateFromUserProfileLanguages(&engine)) &&
        engine != nullptr)
        *language = QStringLiteral("user-profile");

    if (engine == nullptr) {
        *error = tags.isEmpty()
                     ? QStringLiteral("系统没有安装任何 OCR 识别语言包")
                     : QStringLiteral("没有可用的 OCR 引擎（已安装语言：%1）").arg(tags.join(QStringLiteral(", ")));
    }
    return engine;
}

// 从识别结果里读出行与词。
void collectLines(IOcrResultAbi *result, OcrOutcome *outcome) {
    IVectorViewAbi<IOcrLineAbi> *lines = nullptr;
    if (FAILED(result->get_Lines(&lines)) || lines == nullptr) return;
    UINT32 lineCount = 0;
    lines->get_Size(&lineCount);
    for (UINT32 i = 0; i < lineCount; ++i) {
        IOcrLineAbi *line = nullptr;
        if (FAILED(lines->GetAt(i, &line)) || line == nullptr) continue;
        OcrLine entry;
        HSTRING lineText = nullptr;
        if (SUCCEEDED(line->get_Text(&lineText)) && lineText != nullptr) {
            entry.text = hstringToQString(lineText);
            WindowsDeleteString(lineText);
        }
        IVectorViewAbi<IOcrWordAbi> *words = nullptr;
        if (SUCCEEDED(line->get_Words(&words)) && words != nullptr) {
            UINT32 wordCount = 0;
            words->get_Size(&wordCount);
            for (UINT32 w = 0; w < wordCount; ++w) {
                IOcrWordAbi *word = nullptr;
                if (FAILED(words->GetAt(w, &word)) || word == nullptr) continue;
                OcrWord item;
                HSTRING text = nullptr;
                if (SUCCEEDED(word->get_Text(&text)) && text != nullptr) {
                    item.text = hstringToQString(text);
                    WindowsDeleteString(text);
                }
                AbiRect box{};
                if (SUCCEEDED(word->get_BoundingRect(&box))) {
                    // OCR 的框是浮点像素：向外取整，避免把边缘像素漏在框外。
                    const int left = int(std::floor(box.X));
                    const int top = int(std::floor(box.Y));
                    const int right = int(std::ceil(box.X + box.Width));
                    const int bottom = int(std::ceil(box.Y + box.Height));
                    item.rect = QRect(left, top, qMax(1, right - left), qMax(1, bottom - top));
                }
                release(word);
                if (!item.text.trimmed().isEmpty() && item.rect.isValid()) entry.words.append(item);
            }
            release(words);
        }
        release(line);
        if (!entry.words.isEmpty()) outcome->lines.append(entry);
    }
    release(lines);
}

}  // namespace

bool recognizeText(const QImage &image, const QString &preferredText, const std::atomic_bool *cancelled,
                   OcrOutcome *outcome, QString *code, QString *message) {
    const auto fail = [code, message](const QString &failureCode, const QString &failureMessage) {
        if (code) *code = failureCode;
        if (message) *message = failureMessage;
        return false;
    };
    if (outcome == nullptr) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("outcome 为空"));
    *outcome = OcrOutcome{};
    if (image.isNull()) return fail(QStringLiteral("INVALID_ARGUMENT"), QStringLiteral("待识别图像为空"));
    if (cancelled != nullptr && cancelled->load())
        return fail(QStringLiteral("CANCELLED"), QStringLiteral("操作已被取消"));

    QElapsedTimer timer;
    timer.start();

    RoScope ro;
    if (!ro.usable())
        return fail(QStringLiteral("OCR_UNAVAILABLE"),
                    QStringLiteral("OCR 初始化失败（WinRT 不可用），可能处于受限会话"));

    IOcrEngineStaticsAbi *statics = nullptr;
    HRESULT hr = activate(L"Windows.Media.Ocr.OcrEngine", kIID_OcrEngineStatics,
                          reinterpret_cast<void **>(&statics));
    if (FAILED(hr) || statics == nullptr)
        return fail(QStringLiteral("OCR_UNAVAILABLE"),
                    QStringLiteral("无法访问 Windows.Media.Ocr（可能需要安装“可选功能 → 语言”里的 OCR 组件）"));

    UINT32 maxDimension = 0;
    statics->get_MaxImageDimension(&maxDimension);
    if (maxDimension > 0 && (UINT32(image.width()) > maxDimension || UINT32(image.height()) > maxDimension)) {
        release(statics);
        return fail(QStringLiteral("INVALID_ARGUMENT"),
                    QStringLiteral("图像 %1x%2 超过 OCR 上限 %3 像素")
                        .arg(image.width())
                        .arg(image.height())
                        .arg(maxDimension));
    }

    QString language;
    QString engineError;
    IOcrEngineAbi *engine = createEngine(statics, preferredText, &language, &engineError);
    release(statics);
    if (engine == nullptr)
        return fail(QStringLiteral("OCR_UNAVAILABLE"), engineError);

    QString bitmapError;
    IInspectable *bitmap = softwareBitmapFrom(image, &bitmapError);
    if (bitmap == nullptr) {
        release(engine);
        return fail(QStringLiteral("OCR_FAILED"), bitmapError);
    }

    IAsyncOperationAbi<IOcrResultAbi> *operation = nullptr;
    hr = engine->RecognizeAsync(bitmap, &operation);
    release(bitmap);
    release(engine);
    if (FAILED(hr) || operation == nullptr)
        return fail(QStringLiteral("OCR_FAILED"), QStringLiteral("RecognizeAsync 失败（hr=%1）").arg(qlonglong(hr)));

    IAsyncInfoAbi *info = nullptr;
    hr = operation->QueryInterface(kIID_AsyncInfo, reinterpret_cast<void **>(&info));
    if (FAILED(hr) || info == nullptr) {
        release(operation);
        return fail(QStringLiteral("OCR_FAILED"), QStringLiteral("IAsyncInfo 不可用（hr=%1）").arg(qlonglong(hr)));
    }

    int status = kAsyncStarted;
    while (true) {
        info->get_Status(&status);
        if (status != kAsyncStarted) break;
        if (cancelled != nullptr && cancelled->load()) {
            info->Cancel();
            info->Close();
            release(info);
            release(operation);
            return fail(QStringLiteral("CANCELLED"), QStringLiteral("操作已被取消"));
        }
        if (timer.elapsed() > kRecognizeTimeoutMs) {
            info->Cancel();
            info->Close();
            release(info);
            release(operation);
            return fail(QStringLiteral("OCR_FAILED"),
                        QStringLiteral("OCR 识别超过 %1 ms 未完成").arg(kRecognizeTimeoutMs));
        }
        QThread::msleep(2);
    }
    release(info);

    IOcrResultAbi *result = nullptr;
    if (status == kAsyncCompleted) operation->GetResults(&result);
    release(operation);
    if (result == nullptr)
        return fail(QStringLiteral("OCR_FAILED"),
                    QStringLiteral("OCR 识别未完成（状态 %1）").arg(status));

    collectLines(result, outcome);
    release(result);
    outcome->language = language;
    outcome->elapsedMs = static_cast<int>(timer.elapsed());
    return true;
}

void matchOcrText(const OcrOutcome &outcome, const TextQuery &query, const QRect &frameRegion,
                  QVector<TextMatch> *matches) {
    if (matches == nullptr) return;
    const QString needle = normalizeText(query.text);
    if (needle.isEmpty()) return;
    const bool exact = query.match.compare(QLatin1String("exact"), Qt::CaseInsensitive) == 0;
    // 一个目标最多跨 8 个 OCR 词：再长的“短语”更像是整行，交给整行匹配。
    constexpr int kMaxWordsPerWindow = 8;

    QVector<TextMatch> found;
    for (const OcrLine &line : outcome.lines) {
        const int count = line.words.size();
        for (int start = 0; start < count; ++start) {
            QString window;
            QRect box;
            for (int end = start; end < count && end < start + kMaxWordsPerWindow; ++end) {
                window += normalizeText(line.words.at(end).text);
                box = box.isNull() ? line.words.at(end).rect : box.united(line.words.at(end).rect);
                if (window.isEmpty()) continue;
                const bool isExact = window == needle;
                const bool hit = isExact || (!exact && window.contains(needle));
                if (hit) {
                    TextMatch match;
                    match.rect = box;
                    match.name = window;
                    match.controlType = QString();
                    match.source = QStringLiteral("ocr");
                    match.score = isExact ? 1.0 : 0.9;
                    found.append(match);
                    // 精确命中已经是最紧的窗口，不用再往后加词。
                    if (isExact) break;
                    continue;
                }
                // 归一化后已经比目标长出很多，再往后加词只会更不匹配。
                if (window.size() > needle.size() + 8) break;
            }
        }
    }

    if (frameRegion.isValid() && !frameRegion.isEmpty()) {
        QVector<TextMatch> inside;
        for (const TextMatch &match : found) {
            if (match.rect.intersects(frameRegion)) inside.append(match);
        }
        found = inside;
    }

    // 同分时先给框小的（词窗口比整行更接近“那个按钮”），再按阅读顺序。
    std::stable_sort(found.begin(), found.end(), [](const TextMatch &a, const TextMatch &b) {
        if (a.score != b.score) return a.score > b.score;
        const qint64 areaA = qint64(a.rect.width()) * a.rect.height();
        const qint64 areaB = qint64(b.rect.width()) * b.rect.height();
        if (areaA != areaB) return areaA < areaB;
        if (a.rect.top() != b.rect.top()) return a.rect.top() < b.rect.top();
        return a.rect.left() < b.rect.left();
    });

    // 去重：一个框有 ≥60% 面积被已保留的框盖住，就认为它是同一个目标。
    QVector<TextMatch> kept;
    for (const TextMatch &match : found) {
        bool duplicate = false;
        for (const TextMatch &existing : kept) {
            const QRect overlap = match.rect.intersected(existing.rect);
            if (overlap.isEmpty()) continue;
            const qint64 overlapArea = qint64(overlap.width()) * overlap.height();
            const qint64 smaller = qMin(qint64(match.rect.width()) * match.rect.height(),
                                        qint64(existing.rect.width()) * existing.rect.height());
            if (smaller > 0 && overlapArea * 10 >= smaller * 6) {
                duplicate = true;
                break;
            }
        }
        if (!duplicate) kept.append(match);
    }
    *matches = kept;
}

}  // namespace yiuk::vision
