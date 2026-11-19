cmake_minimum_required(VERSION 3.25)
project(avsync LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

find_package(PkgConfig REQUIRED)
pkg_check_modules(FFMPEG REQUIRED IMPORTED_TARGET
  libavformat libavcodec libavutil libswresample)

add_library(avsync_core STATIC
  src/master_clock.cpp
  src/metrics.cpp
  src/sync_controller.cpp
  src/media_source.cpp
  src/audio_output.cpp
  src/video_output.cpp)
target_include_directories(avsync_core PUBLIC src)
target_link_libraries(avsync_core PUBLIC PkgConfig::FFMPEG)
target_compile_options(avsync_core PRIVATE -Wall -Wextra)

add_executable(avsync src/main.cpp)
target_link_libraries(avsync PRIVATE avsync_core)

enable_testing()
add_executable(sync_scenarios tests/sync_scenarios.cpp)
target_link_libraries(sync_scenarios PRIVATE avsync_core)
add_test(NAME sync_scenarios COMMAND sync_scenarios)
