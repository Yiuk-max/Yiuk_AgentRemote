cmake_minimum_required(VERSION 3.21)
project(YiukDeviceAssist VERSION 0.1.8 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTOUIC ON)
set(CMAKE_AUTORCC ON)

find_package(Qt6 6.4 REQUIRED COMPONENTS Widgets Network)

# 视觉/抓屏公共库。设备端、互操作程序与测试程序共用同一份实现，
# 避免 screenshot_id、坐标基准与临时截图生命周期出现两套互相矛盾的逻辑。
add_library(yiuk-vision STATIC
    src/vision/screengrab.cpp
    src/vision/screengrab.h
    src/vision/snapshotstore.cpp
    src/vision/snapshotstore.h
    src/vision/sessionlimiter.cpp
    src/vision/sessionlimiter.h
    src/vision/devicegate.cpp
    src/vision/devicegate.h
    src/vision/textlocator.cpp
    src/vision/textlocator.h
    src/vision/visionservice.cpp
    src/vision/visionservice.h
)
target_include_directories(yiuk-vision PUBLIC src)
# 点击（含变化检测）需要输入内核：vision 依赖 input，反向不依赖。
target_link_libraries(yiuk-vision PUBLIC Qt6::Gui yiuk-input)
if(WIN32)
    # 抓屏走 GDI，文本定位走 UIA（COM）：MinGW 下必须显式链接这些系统库。
    target_link_libraries(yiuk-vision PUBLIC gdi32 user32 ole32 oleaut32 uuid)
    # NOMINMAX 防止 windows.h 的 min/max 宏破坏 std::max；UIA 需要 _WIN32_WINNT >= Win7。
    target_compile_definitions(yiuk-vision PRIVATE
        UNICODE _UNICODE WIN32_LEAN_AND_MEAN NOMINMAX _WIN32_WINNT=0x0A00)
endif()
set_target_properties(yiuk-vision PROPERTIES FOLDER "libs")

# 原生鼠标输入：SendInput 绝对坐标、确定性 ease-in-out 轨迹、按键安全释放、
# 点击前后的局部变化采样。与 vision 分开成两个库：视觉可以单独测试，
# 输入可以单独测试，谁都不需要拉起整个客户端。
add_library(yiuk-input STATIC
    src/input/inputcontroller.cpp
    src/input/inputcontroller.h
)
target_include_directories(yiuk-input PUBLIC src)
target_link_libraries(yiuk-input PUBLIC Qt6::Core)
if(WIN32)
    # sampleRegion 用 DwmFlush 等合成，避免点击后采样到上一帧。
    target_link_libraries(yiuk-input PUBLIC user32 dwmapi)
endif()
set_target_properties(yiuk-input PROPERTIES FOLDER "libs")

qt_add_executable(yiuk-device-assist WIN32
    src/main.cpp
    src/apphelpers.cpp
    src/apphelpers.h
    src/glyph.cpp
    src/glyph.h
    src/titlebar.cpp
    src/titlebar.h
    src/yiukdialog.cpp
    src/yiukdialog.h
    src/userquestion.cpp
    src/userquestion.h
    src/questions.cpp
    src/mainwindow.cpp
    src/mainwindow.h
    src/pages_home.cpp
    src/pages_devices.cpp
    src/pages_settings.cpp
    src/platformlink.cpp
    src/platformevents.cpp
    src/platformevents_vision.cpp
    src/sessionflow.cpp
    src/activity.cpp
    src/activitypanel.cpp
    src/activitypanel.h
    src/localtools.cpp
    src/powershellrunner.cpp
    src/powershellrunner.h
    src/nativeops.cpp
    src/nativeops.h
    src/localbridge.cpp
    src/localbridge.h
    src/platformclient.cpp
    src/platformclient.h
    src/securestore.cpp
    src/securestore.h
    resources/qt-client.qrc
)

if(WIN32)
    enable_language(RC)
    # One generated resource carries the icon plus the file version, so
    # Explorer/setup.exe and the window title can never disagree on the version.
    string(REPLACE "." "," YIUK_RC_FILEVERSION "${PROJECT_VERSION},0")
    # The generated resource carries icons + version info; regenerate it when
    # the artwork changes (windres emits no depfile for ICON statements).
    function(yiuk_add_version_resource target internal filename description)
        set(YIUK_RC_INTERNAL "${internal}")
        set(YIUK_RC_FILENAME "${filename}")
        set(YIUK_RC_DESCRIPTION "${description}")
        set(rc_file "${CMAKE_CURRENT_BINARY_DIR}/${internal}.rc")
        configure_file(resources/app-version.rc.in "${rc_file}" @ONLY)
        set_property(SOURCE "${rc_file}" PROPERTY OBJECT_DEPENDS
            "${CMAKE_CURRENT_SOURCE_DIR}/resources/app.ico;${CMAKE_CURRENT_SOURCE_DIR}/resources/tray.ico;${CMAKE_CURRENT_SOURCE_DIR}/resources/resource.h")
        target_sources(${target} PRIVATE "${rc_file}")
        set(${target}_RC_FILE "${rc_file}" PARENT_SCOPE)
    endfunction()
    yiuk_add_version_resource(yiuk-device-assist "yiuk-device-assist" "yiuk-device-assist.exe" "Yiuk Device Assist")
    target_include_directories(yiuk-device-assist PRIVATE resources)
    target_link_libraries(yiuk-device-assist PRIVATE crypt32)
endif()

# 只读活动日志运行在独立的 Qt-free 进程中：这是原生逐像素透明的 Win32 窗口
# 所有者可随时关闭或重新打开而不影响会话；客户端通过 stdin 向其传输协议行
add_executable(yiuk-activity-panel WIN32
    src/panel/main.cpp
    src/panel/panelpipe.cpp
    src/panel/panelpipe.h
    src/panel/paneltext.cpp
    src/panel/paneltext.h
    src/panel/paneltypes.h
    src/panel/panelwindow.cpp
    src/panel/panelwindow.h
)
target_include_directories(yiuk-activity-panel PRIVATE resources)
if(WIN32)
    yiuk_add_version_resource(yiuk-activity-panel "yiuk-activity-panel" "yiuk-activity-panel.exe" "Yiuk Activity Panel")
endif()
target_compile_definitions(yiuk-activity-panel PRIVATE
    UNICODE _UNICODE WIN32_LEAN_AND_MEAN NOMINMAX _WIN32_WINNT=0x0A00)
target_link_libraries(yiuk-activity-panel PRIVATE gdiplus shell32 user32 gdi32)
if(MINGW)
    # 面板只依赖系统 DLL，因此不链接 MinGW 运行时 DLL
    target_link_options(yiuk-activity-panel PRIVATE -static-libgcc -static-libstdc++)
endif()
add_dependencies(yiuk-device-assist yiuk-activity-panel)

target_link_libraries(yiuk-device-assist PRIVATE Qt6::Widgets Qt6::Network yiuk-vision yiuk-input)

# 将 CMake 项目版本提供给界面，确保支持报告始终注明当前运行的客户端版本
target_compile_definitions(yiuk-device-assist PRIVATE YIUK_CLIENT_VERSION="${PROJECT_VERSION}")

# 将 Agent MCP 适配器与 Qt 客户端一起打包，用户无需安装 Go 或另行下载代码仓库
find_program(YIUK_GO_EXECUTABLE go)
if(YIUK_GO_EXECUTABLE)
    set(YIUK_MCP_OUTPUT "${CMAKE_CURRENT_BINARY_DIR}/yiuk-mcp.exe")
    file(GLOB_RECURSE YIUK_GO_SOURCES CONFIGURE_DEPENDS
        "${CMAKE_CURRENT_SOURCE_DIR}/../cmd/**/*.go"
        "${CMAKE_CURRENT_SOURCE_DIR}/../internal/**/*.go"
        "${CMAKE_CURRENT_SOURCE_DIR}/../go.mod"
        "${CMAKE_CURRENT_SOURCE_DIR}/../go.sum"
    )
    add_custom_command(
        OUTPUT "${YIUK_MCP_OUTPUT}"
        COMMAND "${YIUK_GO_EXECUTABLE}" build -o "${YIUK_MCP_OUTPUT}" ./cmd/yiuk-mcp
        WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}/.."
        DEPENDS ${YIUK_GO_SOURCES}
        COMMENT "Building bundled yiuk-mcp.exe"
        VERBATIM
    )
    add_custom_target(yiuk-mcp ALL DEPENDS "${YIUK_MCP_OUTPUT}")
    add_dependencies(yiuk-device-assist yiuk-mcp)
    set_target_properties(yiuk-mcp PROPERTIES FOLDER "bundled")
else()
    message(WARNING "Go was not found; yiuk-mcp.exe will not be bundled")
endif()

# 可选的无界面互操作测试程序，用于验证与 Go 平台服务端之间的协议
# 使用 -DYIUK_BUILD_TESTS=ON 构建
option(YIUK_BUILD_TESTS "Build the Qt platform interop harness" OFF)
if(YIUK_BUILD_TESTS)
    # Reuse the shipped UI sources so integration tests exercise real styling,
    # authorization and session cleanup, not a parallel mock dialog.
    get_target_property(YIUK_UI_SOURCES yiuk-device-assist SOURCES)
    list(REMOVE_ITEM YIUK_UI_SOURCES src/main.cpp "${yiuk-device-assist_RC_FILE}")
    qt_add_executable(yiuk-question-interop tests/userquestion_interop.cpp ${YIUK_UI_SOURCES})
    set_target_properties(yiuk-question-interop PROPERTIES WIN32_EXECUTABLE FALSE)
    target_include_directories(yiuk-question-interop PRIVATE src)
    target_link_libraries(yiuk-question-interop PRIVATE Qt6::Widgets Qt6::Network yiuk-vision yiuk-input crypt32)
    target_compile_definitions(yiuk-question-interop PRIVATE YIUK_CLIENT_VERSION="${PROJECT_VERSION}")
    add_dependencies(yiuk-question-interop yiuk-activity-panel)

    # 原生操作分发器的行为测试：不需要平台，也不需要 GUI 交互
    qt_add_executable(yiuk-nativeops-test
        tests/nativeops_test.cpp
        src/nativeops.cpp
        src/nativeops.h
    )
    set_target_properties(yiuk-nativeops-test PROPERTIES WIN32_EXECUTABLE FALSE)
    target_include_directories(yiuk-nativeops-test PRIVATE src)
    target_link_libraries(yiuk-nativeops-test PRIVATE Qt6::Core yiuk-vision)

    # 视觉模块的行为测试：快照、会话节流、缩放与图片落盘（不需要平台，不需要 GUI 交互）。
    qt_add_executable(yiuk-vision-test tests/vision_test.cpp)
    set_target_properties(yiuk-vision-test PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(yiuk-vision-test PRIVATE yiuk-vision)

    # 真机验收目标窗口：左半区等点击、右半区等悬停；由互操作程序真实投递输入。
    add_executable(yiuk-input-fixture tests/input_fixture.cpp)
    set_target_properties(yiuk-input-fixture PROPERTIES WIN32_EXECUTABLE FALSE)
    if(WIN32)
        target_link_libraries(yiuk-input-fixture PRIVATE user32 gdi32)
    endif()

    # 原生输入的行为测试：轨迹端点/单调性/可复现性、灰度变化判据、点击区域采样。
    # 不注入键鼠：只验证纯逻辑与"采样能拿到数据"，真实点击由真机验收。
    qt_add_executable(yiuk-input-test tests/input_test.cpp)
    set_target_properties(yiuk-input-test PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(yiuk-input-test PRIVATE yiuk-input)

    qt_add_executable(yiuk-platform-interop
        tests/platform_interop.cpp
        src/platformclient.cpp
        src/platformclient.h
        src/securestore.cpp
        src/securestore.h
        src/nativeops.cpp
        src/nativeops.h
    )
    target_include_directories(yiuk-platform-interop PRIVATE src)
    # 该测试程序是无界面的诊断工具：保留控制台子系统，使 CI 和终端能显示 qInfo/qWarning 输出
    set_target_properties(yiuk-platform-interop PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(yiuk-platform-interop PRIVATE Qt6::Core Qt6::Network yiuk-vision yiuk-input)
    if(WIN32)
        target_link_libraries(yiuk-platform-interop PRIVATE crypt32)
    endif()
endif()

if(MINGW)
    target_link_options(yiuk-device-assist PRIVATE -mwindows)
endif()
