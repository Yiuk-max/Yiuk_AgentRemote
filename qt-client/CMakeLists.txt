cmake_minimum_required(VERSION 3.21)
project(YiukDeviceAssist VERSION 0.1.4 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTOUIC ON)
set(CMAKE_AUTORCC ON)

find_package(Qt6 6.4 REQUIRED COMPONENTS Widgets Network)

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
    src/mainwindow.cpp
    src/mainwindow.h
    src/pages_home.cpp
    src/pages_devices.cpp
    src/pages_settings.cpp
    src/platformlink.cpp
    src/platformevents.cpp
    src/sessionflow.cpp
    src/activity.cpp
    src/activitypanel.cpp
    src/activitypanel.h
    src/localtools.cpp
    src/powershellrunner.cpp
    src/powershellrunner.h
    src/localbridge.cpp
    src/localbridge.h
    src/platformclient.cpp
    src/platformclient.h
    src/securestore.cpp
    src/securestore.h
    resources/qt-client.qrc
)

if(WIN32)
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
target_compile_definitions(yiuk-activity-panel PRIVATE
    UNICODE _UNICODE WIN32_LEAN_AND_MEAN NOMINMAX _WIN32_WINNT=0x0A00)
target_link_libraries(yiuk-activity-panel PRIVATE gdiplus shell32 user32 gdi32)
if(MINGW)
    # 面板只依赖系统 DLL，因此不链接 MinGW 运行时 DLL
    target_link_options(yiuk-activity-panel PRIVATE -static-libgcc -static-libstdc++)
endif()
add_dependencies(yiuk-device-assist yiuk-activity-panel)

target_link_libraries(yiuk-device-assist PRIVATE Qt6::Widgets Qt6::Network)

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
    qt_add_executable(yiuk-platform-interop
        tests/platform_interop.cpp
        src/platformclient.cpp
        src/platformclient.h
        src/securestore.cpp
        src/securestore.h
    )
    target_include_directories(yiuk-platform-interop PRIVATE src)
    # 该测试程序是无界面的诊断工具：保留控制台子系统，使 CI 和终端能显示 qInfo/qWarning 输出
    set_target_properties(yiuk-platform-interop PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(yiuk-platform-interop PRIVATE Qt6::Core Qt6::Network)
    if(WIN32)
        target_link_libraries(yiuk-platform-interop PRIVATE crypt32)
    endif()
endif()

if(MINGW)
    target_link_options(yiuk-device-assist PRIVATE -mwindows)
endif()
