cmake_minimum_required(VERSION 3.21)
project(YiukDeviceAssist VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTOUIC ON)
set(CMAKE_AUTORCC ON)

find_package(Qt6 6.4 REQUIRED COMPONENTS Widgets Network)

qt_add_executable(yiuk-device-assist WIN32
    src/main.cpp
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

target_link_libraries(yiuk-device-assist PRIVATE Qt6::Widgets Qt6::Network)

# Optional headless interop harness used to verify the protocol against the Go
# platform server. Build with -DYIUK_BUILD_TESTS=ON.
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
    target_link_libraries(yiuk-platform-interop PRIVATE Qt6::Core Qt6::Network)
    if(WIN32)
        target_link_libraries(yiuk-platform-interop PRIVATE crypt32)
    endif()
endif()

if(MINGW)
    target_link_options(yiuk-device-assist PRIVATE -mwindows)
endif()
