// Windows.Media.Ocr 可行性探针（无 Qt、无 C++/WinRT、无 MSVC helper）。
//
// 目的：证明 MinGW 工具链能直接调用 WinRT OCR，并给出目标机器的语言包与耗时。
// 全部接口按 ABI 手写，vtable 顺序与 IID 来自 Windows SDK 10.0.26100 的 winrt/*.h，
// 并已在 MinGW 13.1 上逐项验证。SDK 的 winrt 头本身不能在这个工具链下编译
// （Windows.Foundation.h 与 windows.foundation.h 在大小写不敏感的文件系统上互相覆盖），
// 因此手写 ABI 不是偷懒，而是唯一可行路径。
//
// 测试图由 GDI 现场绘制，不读取用户桌面，也不落盘。

#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>

#include <hstring.h>
#include <inspectable.h>
#include <roapi.h>
#include <winstring.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>

namespace {

// ---- WinRT ABI 声明：顺序即 vtable 顺序 ----

MIDL_INTERFACE("ea79a752-f7c2-4265-b1bd-c4dec4e4f080")
ILanguage_: public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_LanguageTag(HSTRING* value) = 0;
};

MIDL_INTERFACE("905a0fef-bc53-11df-8c49-001e4fc686da")
IBufferByteAccess_ : public IUnknown {
   public:
    virtual HRESULT STDMETHODCALLTYPE Buffer(byte** value) = 0;
};

MIDL_INTERFACE("71af914d-c10f-484b-bc50-14bc623b3a27")
IBufferFactory_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE Create(UINT32 capacity, IInspectable** value) = 0;
};

// IVectorView<T>：IInspectable + GetAt + get_Size + IndexOf + GetMany + First。
// 只用前两个方法，声明到 get_Size 即可。
template <class T>
struct IVectorView_ : IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE GetAt(UINT32 index, T** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Size(UINT32* value) = 0;
};

// IAsyncOperation<T>：IInspectable + put_Completed + get_Completed + GetResults。
// 状态轮询在 IAsyncInfo 上，所以这里单独声明它。
template <class T>
struct IAsyncOperation_ : IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE put_Completed(IInspectable* handler) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Completed(IInspectable** handler) = 0;
    virtual HRESULT STDMETHODCALLTYPE GetResults(T** results) = 0;
};

MIDL_INTERFACE("00000036-0000-0000-C000-000000000046")
IAsyncInfo_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_Id(UINT32* value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Status(int* value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_ErrorCode(HRESULT* value) = 0;
    virtual HRESULT STDMETHODCALLTYPE Cancel() = 0;
    virtual HRESULT STDMETHODCALLTYPE Close() = 0;
};

struct AbiRect_ {
    float X;
    float Y;
    float Width;
    float Height;
};

MIDL_INTERFACE("3c2a477a-5cd9-3525-ba2a-23d1e0a68a1d")
IOcrWord_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_BoundingRect(AbiRect_* value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Text(HSTRING* value) = 0;
};

MIDL_INTERFACE("0043a16f-e31f-3a24-899c-d444bd088124")
IOcrLine_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_Words(IVectorView_<IOcrWord_>** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Text(HSTRING* value) = 0;
};

MIDL_INTERFACE("9bd235b2-175b-3d6a-92e2-388c206e2f63")
IOcrResult_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_Lines(IVectorView_<IOcrLine_>** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_TextAngle(IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_Text(HSTRING* value) = 0;
};

MIDL_INTERFACE("5a14bc41-5b76-3140-b680-8825562683ac")
IOcrEngine_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE RecognizeAsync(IInspectable* bitmap, IAsyncOperation_<IOcrResult_>** result) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_RecognizerLanguage(ILanguage_** value) = 0;
};

MIDL_INTERFACE("5bffa85a-3384-3540-9940-699120d428a8")
IOcrEngineStatics_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE get_MaxImageDimension(UINT32* value) = 0;
    virtual HRESULT STDMETHODCALLTYPE get_AvailableRecognizerLanguages(IVectorView_<ILanguage_>** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE IsLanguageSupported(ILanguage_* language, boolean* value) = 0;
    virtual HRESULT STDMETHODCALLTYPE TryCreateFromLanguage(ILanguage_* language, IOcrEngine_** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE TryCreateFromUserProfileLanguages(IOcrEngine_** value) = 0;
};

// ISoftwareBitmapStatics：IInspectable + Copy + Convert + ConvertWithAlpha +
// CreateCopyFromBuffer + CreateCopyWithAlphaFromBuffer + CreateCopyFromSurfaceAsync +
// CreateCopyWithAlphaFromSurfaceAsync。这里只用 CreateCopyFromBuffer（第 4 个自有方法）。
MIDL_INTERFACE("df0385db-672f-4a9d-806e-c2442f343e86")
ISoftwareBitmapStatics_ : public IInspectable {
   public:
    virtual HRESULT STDMETHODCALLTYPE Copy(IInspectable* source, IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE Convert(IInspectable* source, int format, IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE ConvertWithAlpha(IInspectable* source, int format, int alpha,
                                                      IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyFromBuffer(IInspectable* source, int format, INT32 width,
                                                           INT32 height, IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyWithAlphaFromBuffer(IInspectable* source, int format, INT32 width,
                                                                   INT32 height, int alpha,
                                                                   IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyFromSurfaceAsync(IInspectable* surface, IInspectable** value) = 0;
    virtual HRESULT STDMETHODCALLTYPE CreateCopyWithAlphaFromSurfaceAsync(IInspectable* surface, int alpha,
                                                                         IInspectable** value) = 0;
};

// IID 均为上面各接口的 uuid；MinGW 的 MIDL_INTERFACE 不带 uuid，故显式给出。
const GUID kIID_OcrEngineStatics = {0x5bffa85a, 0x3384, 0x3540, {0x99, 0x40, 0x69, 0x91, 0x20, 0xd4, 0x28, 0xa8}};
const GUID kIID_BufferFactory = {0x71af914d, 0xc10f, 0x484b, {0xbc, 0x50, 0x14, 0xbc, 0x62, 0x3b, 0x3a, 0x27}};
const GUID kIID_SoftwareBitmapStatics = {0xdf0385db, 0x672f, 0x4a9d,
                                         {0x80, 0x6e, 0xc2, 0x44, 0x2f, 0x34, 0x3e, 0x86}};
const GUID kIID_AsyncInfo = {0x00000036, 0x0000, 0x0000, {0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}};
const GUID kIID_BufferByteAccess = {0x905a0fef, 0xbc53, 0x11df,
                                    {0x8c, 0x49, 0x00, 0x1e, 0x4f, 0xc6, 0x86, 0xda}};

// BitmapPixelFormat_Bgra8 = 87（SDK 10.0.26100）。
constexpr int kBitmapPixelFormatBgra8 = 87;
constexpr int kAsyncStarted = 0;
constexpr int kAsyncCompleted = 1;

void step(const char* label) {
    static const bool enabled = getenv("YIUK_OCR_TRACE") != nullptr;
    if (!enabled) return;
    std::fprintf(stderr, "[ocr] %s\n", label);
    std::fflush(stderr);
}

double nowMs() {
    using clock = std::chrono::steady_clock;
    static const clock::time_point start = clock::now();
    return std::chrono::duration<double, std::milli>(clock::now() - start).count();
}

template <typename T>
void release(T*& pointer) {
    if (pointer) {
        pointer->Release();
        pointer = nullptr;
    }
}

HRESULT activate(const wchar_t* className, REFIID iid, void** out) {
    // 用分配式 HSTRING：引用式 HSTRING 的生命周期绑定在栈帧上，
    // 而激活工厂可能在调用返回后才读取类名。
    const UINT32 length = UINT32(wcslen(className));
    HSTRING name = nullptr;
    HRESULT hr = WindowsCreateString(className, length, &name);
    if (FAILED(hr)) return hr;
    hr = RoGetActivationFactory(name, iid, out);
    WindowsDeleteString(name);
    return hr;
}

std::string utf8(HSTRING value) {
    if (!value) return {};
    UINT32 length = 0;
    const wchar_t* raw = WindowsGetStringRawBuffer(value, &length);
    if (!raw || length == 0) return {};
    const int needed = WideCharToMultiByte(CP_UTF8, 0, raw, int(length), nullptr, 0, nullptr, nullptr);
    std::string out(size_t(needed > 0 ? needed : 0), '\0');
    if (needed > 0) WideCharToMultiByte(CP_UTF8, 0, raw, int(length), out.data(), needed, nullptr, nullptr);
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

// 用 GDI 画一张固定测试图：中英混排 + 一个按钮，避免依赖用户桌面。
struct TestImage {
    int width = 0;
    int height = 0;
    std::string bgra;  // 自上而下的 BGRA
};

TestImage renderTestImage(int width, int height) {
    TestImage image;
    image.width = width;
    image.height = height;
    image.bgra.assign(size_t(width) * size_t(height) * 4, 0);
    BITMAPINFO info{};
    info.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    info.bmiHeader.biWidth = width;
    info.bmiHeader.biHeight = -height;
    info.bmiHeader.biPlanes = 1;
    info.bmiHeader.biBitCount = 32;
    info.bmiHeader.biCompression = BI_RGB;
    void* bits = nullptr;
    HDC screen = GetDC(nullptr);
    HDC memory = CreateCompatibleDC(screen);
    HBITMAP bitmap = CreateDIBSection(screen, &info, DIB_RGB_COLORS, &bits, nullptr, 0);
    if (!memory || !bitmap || !bits) {
        if (bitmap) DeleteObject(bitmap);
        if (memory) DeleteDC(memory);
        ReleaseDC(nullptr, screen);
        return image;
    }
    HGDIOBJ previous = SelectObject(memory, bitmap);
    RECT full{0, 0, width, height};
    FillRect(memory, &full, static_cast<HBRUSH>(GetStockObject(WHITE_BRUSH)));
    SetBkMode(memory, TRANSPARENT);
    SetTextColor(memory, RGB(20, 30, 45));

    LOGFONTW font{};
    font.lfHeight = -40;
    lstrcpyW(font.lfFaceName, L"Microsoft YaHei UI");
    HFONT textFont = CreateFontIndirectW(&font);
    HGDIOBJ oldFont = SelectObject(memory, textFont);
    const wchar_t* line1 = L"收藏 首页 设置";
    TextOutW(memory, 40, 40, line1, lstrlenW(line1));
    const wchar_t* line2 = L"Settings 2026 设备已连接";
    TextOutW(memory, 40, 120, line2, lstrlenW(line2));

    RECT button{40, 200, 260, 260};
    HBRUSH buttonBrush = CreateSolidBrush(RGB(20, 121, 237));
    FillRect(memory, &button, buttonBrush);
    DeleteObject(buttonBrush);
    SetTextColor(memory, RGB(255, 255, 255));
    const wchar_t* buttonText = L"确定";
    TextOutW(memory, 60, 212, buttonText, lstrlenW(buttonText));

    SelectObject(memory, oldFont);
    DeleteObject(textFont);
    GdiFlush();
    memcpy(image.bgra.data(), bits, image.bgra.size());
    SelectObject(memory, previous);
    DeleteObject(bitmap);
    DeleteDC(memory);
    ReleaseDC(nullptr, screen);
    return image;
}

// 把 BGRA 像素包装成 SoftwareBitmap（OCR 只接受 SoftwareBitmap）。
IInspectable* softwareBitmapFrom(const TestImage& image, std::string* error) {
    IBufferFactory_* bufferFactory = nullptr;
    HRESULT hr = activate(L"Windows.Storage.Streams.Buffer", kIID_BufferFactory,
                          reinterpret_cast<void**>(&bufferFactory));
    if (FAILED(hr) || !bufferFactory) {
        *error = "Buffer activation hr=" + std::to_string(long(hr));
        return nullptr;
    }
    const UINT32 capacity = UINT32(image.bgra.size());
    IInspectable* buffer = nullptr;
    hr = bufferFactory->Create(capacity, &buffer);
    release(bufferFactory);
    if (FAILED(hr) || !buffer) {
        *error = "IBufferFactory::Create hr=" + std::to_string(long(hr));
        return nullptr;
    }

    IBufferByteAccess_* byteAccess = nullptr;
    hr = buffer->QueryInterface(kIID_BufferByteAccess, reinterpret_cast<void**>(&byteAccess));
    if (FAILED(hr) || !byteAccess) {
        *error = "QI IBufferByteAccess hr=" + std::to_string(long(hr));
        release(buffer);
        return nullptr;
    }
    byte* bytes = nullptr;
    hr = byteAccess->Buffer(&bytes);
    if (FAILED(hr) || !bytes) {
        *error = "IBufferByteAccess::Buffer hr=" + std::to_string(long(hr));
        release(byteAccess);
        release(buffer);
        return nullptr;
    }
    memcpy(bytes, image.bgra.data(), image.bgra.size());
    release(byteAccess);

    // Create 只设置 Capacity；CreateCopyFromBuffer 依据 Length 判断数据长度，必须补写。
    {
        void** bufferVtable = *reinterpret_cast<void***>(buffer);
        typedef HRESULT(STDMETHODCALLTYPE * PutLengthFn)(void*, UINT32);
        const auto putLength = reinterpret_cast<PutLengthFn>(bufferVtable[8]);
        hr = putLength(buffer, capacity);
        if (FAILED(hr)) {
            *error = "IBuffer::put_Length hr=" + std::to_string(long(hr));
            release(buffer);
            return nullptr;
        }
    }

    ISoftwareBitmapStatics_* bitmapStatics = nullptr;
    hr = activate(L"Windows.Graphics.Imaging.SoftwareBitmap", kIID_SoftwareBitmapStatics,
                  reinterpret_cast<void**>(&bitmapStatics));
    if (FAILED(hr) || !bitmapStatics) {
        *error = "SoftwareBitmap statics hr=" + std::to_string(long(hr));
        release(buffer);
        return nullptr;
    }
    IInspectable* bitmap = nullptr;
    hr = bitmapStatics->CreateCopyFromBuffer(buffer, kBitmapPixelFormatBgra8, image.width, image.height, &bitmap);
    release(bitmapStatics);
    release(buffer);
    if (FAILED(hr) || !bitmap) {
        *error = "CreateCopyFromBuffer hr=" + std::to_string(long(hr));
        return nullptr;
    }
    return bitmap;
}

}  // namespace

int main() {
    const HRESULT ro = RoInitialize(RO_INIT_MULTITHREADED);
    if (FAILED(ro)) {
        std::printf("{\"ok\":false,\"error\":\"RoInitialize\",\"hr\":%ld}\n", long(ro));
        return 1;
    }

    IOcrEngineStatics_* statics = nullptr;
    HRESULT hr = activate(L"Windows.Media.Ocr.OcrEngine", kIID_OcrEngineStatics,
                          reinterpret_cast<void**>(&statics));
    if (FAILED(hr) || !statics) {
        std::printf("{\"ok\":false,\"error\":\"OcrEngine activation\",\"hr\":%ld}\n", long(hr));
        RoUninitialize();
        return 1;
    }

    step("statics-ok");
    UINT32 maxDimension = 0;
    statics->get_MaxImageDimension(&maxDimension);
    step("maxdimension-ok");

    std::string languagesJson;
    int languageCount = 0;
    IOcrEngine_* engine = nullptr;
    std::string engineLanguage;
    bool engineFromProfile = false;
    IVectorView_<ILanguage_>* available = nullptr;
    if (SUCCEEDED(statics->get_AvailableRecognizerLanguages(&available)) && available) {
        UINT32 size = 0;
        available->get_Size(&size);
        languageCount = int(size);
        for (UINT32 i = 0; i < size; ++i) {
            ILanguage_* language = nullptr;
            if (FAILED(available->GetAt(i, &language)) || !language) continue;
            HSTRING tag = nullptr;
            std::string tagText;
            if (SUCCEEDED(language->get_LanguageTag(&tag)) && tag) {
                tagText = utf8(tag);
                WindowsDeleteString(tag);
            }
            if (!languagesJson.empty()) languagesJson += ",";
            languagesJson += "\"" + jsonEscape(tagText) + "\"";
            if (!engine && tagText.rfind("zh", 0) == 0) {
                if (SUCCEEDED(statics->TryCreateFromLanguage(language, &engine)) && engine) {
                    engineLanguage = tagText;
                }
            }
            release(language);
        }
        release(available);
    }
    if (!engine && SUCCEEDED(statics->TryCreateFromUserProfileLanguages(&engine)) && engine) {
        engineFromProfile = true;
        engineLanguage = "user-profile";
    }
    if (!engine) {
        std::printf(
            "{\"ok\":false,\"error\":\"OCR_UNAVAILABLE\",\"languages\":[%s],\"max_dimension\":%u,"
            "\"hint\":\"no recognizer language installed\"}\n",
            languagesJson.c_str(), unsigned(maxDimension));
        release(statics);
        RoUninitialize();
        return 2;
    }

    step("engine-ready");
    const TestImage image = renderTestImage(900, 340);
    step("image-ready");
    std::string bitmapError;
    IInspectable* bitmap = softwareBitmapFrom(image, &bitmapError);
    if (!bitmap) {
        std::printf("{\"ok\":false,\"error\":\"BITMAP_FAILED\",\"detail\":\"%s\"}\n",
                    jsonEscape(bitmapError).c_str());
        release(engine);
        release(statics);
        RoUninitialize();
        return 1;
    }

    step("bitmap-ready");
    IAsyncOperation_<IOcrResult_>* operation = nullptr;
    const double recognizeStart = nowMs();
    hr = engine->RecognizeAsync(bitmap, &operation);
    double recognizeMs = 0.0;
    int finalStatus = -1;
    IOcrResult_* result = nullptr;
    if (SUCCEEDED(hr) && operation) {
        IAsyncInfo_* info = nullptr;
        if (SUCCEEDED(operation->QueryInterface(kIID_AsyncInfo, reinterpret_cast<void**>(&info))) && info) {
            int status = kAsyncStarted;
            while (true) {
                info->get_Status(&status);
                if (status != kAsyncStarted) break;
                Sleep(2);
            }
            finalStatus = status;
            release(info);
        }
        recognizeMs = nowMs() - recognizeStart;
        if (finalStatus == kAsyncCompleted) operation->GetResults(&result);
    }

    step("recognize-done");
    int lineCount = 0;
    int wordCount = 0;
    std::string textPreview;
    std::string firstLine;
    AbiRect_ firstWord{};
    bool hasWord = false;
    if (result) {
        HSTRING text = nullptr;
        if (SUCCEEDED(result->get_Text(&text)) && text) {
            const std::string full = utf8(text);
            textPreview = full.substr(0, std::min<size_t>(160, full.size()));
            WindowsDeleteString(text);
        }
        IVectorView_<IOcrLine_>* lines = nullptr;
        if (SUCCEEDED(result->get_Lines(&lines)) && lines) {
            UINT32 size = 0;
            lines->get_Size(&size);
            lineCount = int(size);
            if (size > 0) {
                IOcrLine_* line = nullptr;
                if (SUCCEEDED(lines->GetAt(0, &line)) && line) {
                    HSTRING lineText = nullptr;
                    if (SUCCEEDED(line->get_Text(&lineText)) && lineText) {
                        firstLine = utf8(lineText);
                        WindowsDeleteString(lineText);
                    }
                    IVectorView_<IOcrWord_>* words = nullptr;
                    if (SUCCEEDED(line->get_Words(&words)) && words) {
                        UINT32 wordSize = 0;
                        words->get_Size(&wordSize);
                        wordCount = int(wordSize);
                        IOcrWord_* word = nullptr;
                        if (wordSize > 0 && SUCCEEDED(words->GetAt(0, &word)) && word) {
                            hasWord = SUCCEEDED(word->get_BoundingRect(&firstWord));
                            release(word);
                        }
                        release(words);
                    }
                    release(line);
                }
            }
            release(lines);
        }
        release(result);
    }
    release(operation);
    release(bitmap);
    release(engine);
    release(statics);
    RoUninitialize();

    std::printf(
        "{\"ok\":true,\"languages\":[%s],\"language_count\":%d,\"engine_language\":\"%s\","
        "\"engine_from_profile\":%s,\"max_dimension\":%u,\"image\":{\"width\":%d,\"height\":%d},"
        "\"recognize_ms\":%.1f,\"async_status\":%d,\"lines\":%d,\"words_first_line\":%d,"
        "\"first_word_bbox\":[%.1f,%.1f,%.1f,%.1f],\"first_line\":\"%s\",\"text_preview\":\"%s\"}\n",
        languagesJson.c_str(), languageCount, jsonEscape(engineLanguage).c_str(),
        engineFromProfile ? "true" : "false", unsigned(maxDimension), image.width, image.height, recognizeMs,
        finalStatus, lineCount, wordCount, hasWord ? double(firstWord.X) : -1.0,
        hasWord ? double(firstWord.Y) : -1.0, hasWord ? double(firstWord.Width) : -1.0,
        hasWord ? double(firstWord.Height) : -1.0, jsonEscape(firstLine).c_str(),
        jsonEscape(textPreview).c_str());
    return 0;
}
