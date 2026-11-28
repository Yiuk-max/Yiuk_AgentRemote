# 第三方组件与许可

发布包里到底带了什么、许可证是什么、版本钉在哪儿。**改依赖时同时改这张表和打包说明。**

## 1. 发布包里带的

Windows 绿色包（`Release\YiukDeviceAssist-<version>-win64`）里只有三个 exe + Qt 运行库：

| 组件 | 版本 | 许可证 | 怎么进来的 |
| --- | --- | --- | --- |
| Qt（Core / Gui / Widgets / Network / Svg） | 6.11.1 | LGPL-3.0（或商业许可） | `windeployqt` 拷贝动态库；不修改 Qt 源码，允许用户替换 DLL |
| Qt 插件（platforms / styles / imageformats / tls / generic） | 6.11.1 | 同上 | 同上 |
| MinGW-w64 运行库（`libgcc_s_seh-1` / `libstdc++-6` / `libwinpthread-1`） | GCC 13.1.0 | GPL-3.0 **+ GCC Runtime Library Exception** | 编译器自带，随 exe 分发属例外条款覆盖 |
| OpenCV（core / imgproc / zlib） | 4.12.0 | Apache-2.0 | **静态**链接进 `yiuk-device-assist.exe`（灰度模板匹配），因此发布说明需要附 Apache-2.0 声明 |
| D3Dcompiler_47 / opengl32sw | 随 Qt | 微软 / Qt 再分发条款 | `windeployqt` 按需带上 |
| Go 标准库 + `santhosh-tekuri/jsonschema` | 见 `go.mod` | BSD-3-Clause / Apache-2.0 | 静态编进 `yiuk-mcp.exe`（纯 Go，无 CGO） |
| golang.org/x/net 等间接依赖 | 见 `go.sum` | BSD-3-Clause | 同上 |

说明：

* `yiuk-activity-panel.exe` 只依赖系统 DLL（user32/gdi32）与 MinGW 运行库。
* `yiuk-mcp.exe` 是 `CGO_ENABLED=0` 的 Go 静态产物，不需要 Qt。
* 没有任何 GPL-only 的运行时组件被链接进发布包；Qt 以动态库形式分发，满足 LGPL 的替换要求。
  如果要闭源分发**且**使用静态 Qt，需要换成商业许可——这一条留给发行方确认。

## 2. 只在开发/构建时用到的

| 组件 | 版本 | 用途 |
| --- | --- | --- |
| CMake / Ninja | 4.3.1 / 1.12.1 | 构建 |
| MinGW-w64 GCC | 13.1.0 posix-seh | 编译（Qt 官方工具链同版本） |
| Windows SDK 头（`uiautomation.h`、`hstring.h`、`roapi.h`） | 10.0.26100 | UIA / WinRT OCR 的 ABI；**不进发布包** |
| Windows.Media.Ocr | 系统组件 | OCR 兜底，用设备上已安装的语言包；不需要随包分发 |
| Redis（可选 `cmd/dev-redis`） | miniredis | 本地开发时的路由表 |

## 3. 检查方式

```powershell
# 发布目录里出现了哪些 DLL / exe
Get-ChildItem Release\YiukDeviceAssist-0.1.9-win64 -Recurse -Include *.dll,*.exe |
  Select-Object Name, VersionInfo.FileVersion

# 主客户端链接了哪些 OpenCV 静态库（应只有 core / imgproc / zlib）
Select-String -Path qt-client\build-qt\build.ninja -Pattern "libopencv_|libzlib" |
  Select-Object -First 5
```
