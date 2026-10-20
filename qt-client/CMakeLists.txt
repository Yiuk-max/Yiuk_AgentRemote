cmake_minimum_required(VERSION 3.21)
project(YiukDeviceAssist VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTOUIC ON)
set(CMAKE_AUTORCC ON)

find_package(Qt6 6.8 REQUIRED COMPONENTS Widgets Network)

qt_add_executable(yiuk-device-assist WIN32
    src/main.cpp
    src/localbridge.cpp
    src/localbridge.h
    resources/qt-client.qrc
)

target_link_libraries(yiuk-device-assist PRIVATE Qt6::Widgets Qt6::Network)

if(MINGW)
    target_link_options(yiuk-device-assist PRIVATE -mwindows)
endif()
