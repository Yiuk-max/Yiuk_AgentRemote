#include "panelpipe.h"

#include <process.h>

#include <string>

namespace yiuk {
namespace {

std::wstring DecodeUtf8(const std::string& bytes) {
    if (bytes.empty()) return std::wstring();
    const int size = MultiByteToWideChar(CP_UTF8, 0, bytes.data(), static_cast<int>(bytes.size()), nullptr, 0);
    if (size <= 0) return std::wstring();
    std::wstring text(static_cast<size_t>(size), L'\0');
    MultiByteToWideChar(CP_UTF8, 0, bytes.data(), static_cast<int>(bytes.size()), text.data(), size);
    return text;
}

unsigned __stdcall ReaderMain(void* argument) {
    const HWND window = static_cast<HWND>(argument);
    const HANDLE pipe = GetStdHandle(STD_INPUT_HANDLE);
    if (pipe == nullptr || pipe == INVALID_HANDLE_VALUE) {
        PostMessageW(window, kMessageClosed, 0, 0);
        return 0;
    }
    std::string pending;
    char chunk[4096];
    for (;;) {
        DWORD read = 0;
        if (!ReadFile(pipe, chunk, sizeof chunk, &read, nullptr) || read == 0) break;
        pending.append(chunk, read);
        size_t newline = 0;
        while ((newline = pending.find('\n')) != std::string::npos) {
            std::wstring line = DecodeUtf8(pending.substr(0, newline));
            pending.erase(0, newline + 1);
            if (!line.empty() && line.back() == L'\r') line.pop_back();
            if (line.empty()) continue;
            auto* payload = new std::wstring(std::move(line));
            if (!PostMessageW(window, kMessageLine, reinterpret_cast<WPARAM>(payload), 0))
                delete payload;
        }
    }
    PostMessageW(window, kMessageClosed, 0, 0);
    return 0;
}

}  // namespace

bool StartStdinReader(HWND window) {
    const HANDLE pipe = GetStdHandle(STD_INPUT_HANDLE);
    if (pipe == nullptr || pipe == INVALID_HANDLE_VALUE) return false;
    const uintptr_t handle = _beginthreadex(nullptr, 0, ReaderMain, window, 0, nullptr);
    return handle != 0;
}

}  // namespace yiuk
