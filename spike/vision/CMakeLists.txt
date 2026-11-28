cmake_minimum_required(VERSION 3.21)
project(YiukVisionSpike LANGUAGES CXX)

# P0：视觉与鼠标增强的可行性测试。目标不是产品代码，而是回答四个问题：
#   1) OpenCV 静态构建在 MinGW + Qt 下是否可链接，体积/耗时是否达标；
#   2) 手写 NCC 能否达到同一耗时线；
#   3) UIA / Windows.Media.Ocr 在 MinGW 工具链下能否直接调用；
#   4) 现有 GDI 抓屏方案在真实桌面上的耗时与几何口径。
#
# 用法见 README.md。每个目标都可独立构建。

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC OFF)

find_package(Qt6 REQUIRED COMPONENTS Core Gui)

option(YIUK_SPIKE_OPENCV "Build the OpenCV matcher when OpenCV is found" ON)
option(YIUK_SPIKE_SSE2 "Use SSE2 intrinsics in the hand-written NCC" ON)

add_library(yiuk_vision_spike STATIC
    src/vision_common.cpp
    src/gray_image.cpp
    src/ncc_matcher.cpp
    src/fixtures.cpp
)
target_include_directories(yiuk_vision_spike PUBLIC src)
target_link_libraries(yiuk_vision_spike PUBLIC Qt6::Core Qt6::Gui)
if(WIN32)
    target_link_libraries(yiuk_vision_spike PUBLIC psapi)
endif()
if(YIUK_SPIKE_SSE2 AND (CMAKE_CXX_COMPILER_ID STREQUAL "GNU" OR CMAKE_CXX_COMPILER_ID MATCHES "Clang"))
    # x86_64 基线已有 SSE2，显式声明便于说明测量口径
    target_compile_options(yiuk_vision_spike PRIVATE -msse2)
endif()

# 手写 NCC：exact（每个尺度全屏扫描）与 pyramid（粗搜 + 精修）
add_executable(vision_bench src/bench_vision.cpp)
target_link_libraries(vision_bench PRIVATE yiuk_vision_spike)

# NCC 自检：参考实现 vs SSE2 / 积分图实现
add_executable(vision_selftest src/selftest_ncc.cpp)
target_link_libraries(vision_selftest PRIVATE yiuk_vision_spike)

if(YIUK_SPIKE_OPENCV)
    find_package(OpenCV QUIET COMPONENTS core imgproc)
    if(OpenCV_FOUND)
        message(STATUS "OpenCV ${OpenCV_VERSION} found: ${OpenCV_DIR}")
        target_sources(vision_bench PRIVATE src/opencv_matcher.cpp)
        target_include_directories(vision_bench PRIVATE src ${OpenCV_INCLUDE_DIRS})
        target_link_libraries(vision_bench PRIVATE ${OpenCV_LIBS})
        target_compile_definitions(vision_bench PRIVATE YIUK_SPIKE_HAVE_OPENCV=1)
    else()
        message(STATUS "OpenCV not found; building without the OpenCV route")
    endif()
endif()

# 生成固定素材：3 种 DPI × 2 种主题的场景 + 模板 + 负样本
add_executable(make_fixtures src/make_fixtures.cpp)
target_link_libraries(make_fixtures PRIVATE yiuk_vision_spike)

# UIA：COM 直连，不经过 PowerShell
add_executable(probe_uia src/probe_uia.cpp)
target_link_libraries(probe_uia PRIVATE ole32 oleaut32 uuid)

# GDI 抓屏：真实桌面的抓取耗时与几何换算（不落盘）
add_executable(probe_screen src/probe_screen.cpp)
target_link_libraries(probe_screen PRIVATE yiuk_vision_spike gdi32)

# Windows.Media.Ocr：自包含 ABI 声明，无 Qt、无 Windows SDK winrt 头。
# 理由见 src/probe_winrt_ocr.cpp 顶部注释。
if(WIN32)
    add_executable(probe_winrt_ocr src/probe_winrt_ocr.cpp)
    target_link_libraries(probe_winrt_ocr PRIVATE windowsapp ole32 oleaut32 gdi32)
    target_compile_definitions(probe_winrt_ocr PRIVATE UNICODE _UNICODE)
    if(CMAKE_CXX_COMPILER_ID STREQUAL "GNU")
        # MinGW GCC 13.1 在 -O2/-O3 会把“声明为纯虚的 WinRT 接口调用”
        # 反优化成 __cxa_pure_virtual（运行期 0xC0000005）。加这个开关可正常工作，
        # 也可以改用显式 vtable 调用。详见 README 的“工具链”一节。
        target_compile_options(probe_winrt_ocr PRIVATE -fno-devirtualize)
    endif()
endif()
