cmake_minimum_required(VERSION 3.28)

if(APPLE AND NOT DEFINED CMAKE_OSX_DEPLOYMENT_TARGET)
  set(CMAKE_OSX_DEPLOYMENT_TARGET "12.0" CACHE STRING "Minimum macOS version")
endif()

project(subtitle_metrics LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT APPLE)
  message(FATAL_ERROR "subtitle-metrics uses CoreFoundation and requires macOS")
endif()

find_package(Freetype REQUIRED)

# Filled in as modules land; keep this list buildable after every commit.
set(SUBTITLE_METRICS_SOURCES
  src/main.cpp
  src/json.cpp
  src/manifest.cpp
  src/unicode.cpp
  src/font_metrics.cpp
  src/line_breaker.cpp
  src/timing.cpp
  src/exporter.cpp
  tests/fixtures.cpp
)

add_executable(subtitle-metrics ${SUBTITLE_METRICS_SOURCES})
target_include_directories(subtitle-metrics PRIVATE src tests)
target_link_libraries(subtitle-metrics PRIVATE Freetype::Freetype
  "-framework CoreFoundation")
target_compile_options(subtitle-metrics PRIVATE -Wall -Wextra -Wpedantic)
