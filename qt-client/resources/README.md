# 客户端品牌图标

- `icon.jpg`：用户提供的原始图片，不直接修改。
- `app.ico`：原图制成的圆角软件图标，用于主客户端 / 活动面板窗口、任务栏、EXE、快捷方式及安装器。
- `tray.svg`：手工简化的实色头像，深蓝发型、浅色脸、粗半眯眼和亮蓝蝴蝶结；透明背景、浅色外缘兼顾深浅任务栏，取消难辨认的细线。
- `tray.ico`：同一头像按尺寸分别渲染，针对 16px 对齐眼睛和蝴蝶结，用 BOX 抗锯齿避免细线缩放的虚边；供 Qt 和原生 Win32 托盘使用，不依赖运行时 SVG 插件。
- 两个 ICO 均包含 `16 / 20 / 24 / 32 / 40 / 48 / 64 / 128 / 256` 像素，覆盖常见 DPI。

## 更新图标

在安装了 Pillow 的 Python 环境执行（仅生成资源时需要，不增加客户端运行 / CMake 构建依赖）：

```sh
python -m pip install Pillow
python qt-client/resources/generate_icons.py
cmake --build qt-client/build-qt
```

替换原画只需更新 `icon.jpg`；调整托盘头像请修改 `generate_icons.py` 中的 `LAYERS`（填色和轮廓）后重新生成，不要只修改生成后的 SVG。

`qt-client.qrc` 将 ICO 嵌入 Qt 主程序；`app-version.rc.in` 由 CMake 按项目版本生成两个 EXE 的资源文件（图标 + 文件版本）。软件图标资源 ID 为 1，保持它排在托盘图标之前，确保资源管理器和快捷方式默认使用原画。

安装器通过 `SetupIconFile` 使用 `app.ico`，需另行编译 Inno Setup 脚本。Windows 可能缓存旧 EXE / 快捷方式图标；验证时优先使用新路径，或取消固定后重新固定到任务栏。
