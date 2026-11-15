cmake_minimum_required(VERSION 3.16)
project(subtitle_metrics LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

find_package(Freetype REQUIRED)
find_package(nlohmann_json REQUIRED)

# Core library: sources are appended per module.
add_library(subtitle_core STATIC
  src/font_metrics.cpp
  src/parser.cpp
  src/line_breaker.cpp
  src/layout.cpp
  src/summary_exporter.cpp
  src/fixture_generator.cpp
)
target_include_directories(subtitle_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_link_libraries(subtitle_core PUBLIC Freetype::Freetype nlohmann_json::nlohmann_json)

enable_testing()

add_executable(subtitle-metrics src/main.cpp)
target_link_libraries(subtitle-metrics PRIVATE subtitle_core)
