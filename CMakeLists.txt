cmake_minimum_required(VERSION 3.20)

project(subtitle_metrics
    VERSION 1.0.0
    DESCRIPTION "Offline multilingual subtitle layout metrics tool"
    LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
    set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

find_package(Freetype 2.13 REQUIRED)
find_package(nlohmann_json 3.11 REQUIRED)

add_executable(subtitle-metrics src/main.cpp)
target_compile_options(subtitle-metrics PRIVATE -Wall -Wextra -Wpedantic)
target_include_directories(subtitle-metrics PRIVATE src)
target_link_libraries(subtitle-metrics
    PRIVATE Freetype::Freetype nlohmann_json::nlohmann_json)
