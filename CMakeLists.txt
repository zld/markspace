cmake_minimum_required(VERSION 3.25)
project(avsync LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release CACHE STRING "" FORCE)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

# FFmpeg 7.1+ ships libavcodec 61 / libavformat 61 / libavutil 59 / libswresample 5.
find_package(PkgConfig REQUIRED)
pkg_check_modules(FFMPEG REQUIRED IMPORTED_TARGET
  libavformat>=61.0
  libavcodec>=61.0
  libavutil>=59.0
  libswresample>=5.0)

add_library(avsync_core STATIC
  src/common.cpp
  src/master_clock.cpp
  src/synchronizer.cpp
  src/demuxer.cpp
  src/decoder.cpp
  src/metrics.cpp
  src/audio_path.cpp
  src/video_path.cpp
  src/pipeline.cpp)
target_include_directories(avsync_core PUBLIC
  ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_link_libraries(avsync_core PUBLIC PkgConfig::FFMPEG)

add_executable(avsync src/main.cpp)
target_link_libraries(avsync PRIVATE avsync_core)

enable_testing()
