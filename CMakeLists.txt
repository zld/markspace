cmake_minimum_required(VERSION 3.27)
project(pw_sync LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
    set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

# ---------------------------------------------------------------------------
# FFmpeg: prefer pkg-config (works with Homebrew on arm64 and x86_64), fall
# back to explicit library/header lookup. The code targets the modern API
# surface shared by FFmpeg 7.x, 8.x and 9.x (libavcodec major >= 61).
# ---------------------------------------------------------------------------
find_package(PkgConfig QUIET)
set(PW_FFMPEG_LIBS "")
if(PkgConfig_FOUND)
    pkg_check_modules(FFMPEG QUIET IMPORTED_TARGET
        libavcodec>=61 libavformat>=61 libavutil>=59 libswresample>=5)
    if(FFMPEG_FOUND)
        set(PW_FFMPEG_LIBS PkgConfig::FFMPEG)
    endif()
endif()

if(NOT PW_FFMPEG_LIBS)
    find_path(FFMPEG_INCLUDE_DIR libavcodec/avcodec.h
        HINTS /opt/homebrew/include /usr/local/include /usr/include)
    find_library(AVCODEC_LIB avcodec
        HINTS /opt/homebrew/lib /usr/local/lib /usr/lib)
    find_library(AVFORMAT_LIB avformat HINTS /opt/homebrew/lib /usr/local/lib /usr/lib)
    find_library(AVUTIL_LIB avutil HINTS /opt/homebrew/lib /usr/local/lib /usr/lib)
    find_library(SWRESAMPLE_LIB swresample HINTS /opt/homebrew/lib /usr/local/lib /usr/lib)
    if(NOT FFMPEG_INCLUDE_DIR OR NOT AVCODEC_LIB OR NOT AVFORMAT_LIB
       OR NOT AVUTIL_LIB OR NOT SWRESAMPLE_LIB)
        message(FATAL_ERROR
            "FFmpeg development libraries (libavcodec/libavformat/libavutil/"
            "libswresample, major versions for FFmpeg 7+) were not found. "
            "Install with: brew install ffmpeg")
    endif()
    add_library(pwsync_ffmpeg INTERFACE)
    target_include_directories(pwsync_ffmpeg INTERFACE ${FFMPEG_INCLUDE_DIR})
    target_link_libraries(pwsync_ffmpeg INTERFACE
        ${AVCODEC_LIB} ${AVFORMAT_LIB} ${AVUTIL_LIB} ${SWRESAMPLE_LIB})
    set(PW_FFMPEG_LIBS pwsync_ffmpeg)
endif()

# Reject ancient FFmpeg at configure time when version headers are visible.
if(DEFINED FFMPEG_libavcodec_VERSION AND FFMPEG_libavcodec_VERSION VERSION_LESS "61")
    message(FATAL_ERROR "FFmpeg 7.x or newer (libavcodec >= 61) is required.")
endif()

# ---------------------------------------------------------------------------
# Core library: diagnostics engine. FFmpeg-dependent translation units are
# split so the pure C++ pieces stay testable in isolation.
# ---------------------------------------------------------------------------
add_library(pwsync_core STATIC
    src/media_types.cpp
    src/clock.cpp
    src/synchronizer.cpp
    src/audio_resample.cpp
    src/demux_decode.cpp
    src/path_simulator.cpp
    src/report.cpp
)
target_include_directories(pwsync_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_compile_options(pwsync_core PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(pwsync_core PUBLIC ${PW_FFMPEG_LIBS})

enable_testing()
