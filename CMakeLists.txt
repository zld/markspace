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

add_library(submetrics_core STATIC
    src/utf8.cpp
    src/config.cpp
    src/font_metrics.cpp
    src/line_breaker.cpp
    src/analyzer.cpp
    src/subtitle_io.cpp
    src/fixture.cpp
    src/json_writer.cpp
)
target_include_directories(submetrics_core PUBLIC src)
target_compile_options(submetrics_core PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(submetrics_core
    PUBLIC Freetype::Freetype nlohmann_json::nlohmann_json)

add_executable(subtitle-metrics src/main.cpp)
target_compile_options(subtitle-metrics PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(subtitle-metrics PRIVATE submetrics_core)

# Test-only adversarial sample generator; not part of the shipped tool.
option(BUILD_TESTING "Build test helper executables" ON)
if(BUILD_TESTING)
    add_executable(fixture-samples tests/fixtures.cpp)
    target_compile_options(fixture-samples PRIVATE -Wall -Wextra -Wpedantic)
endif()

enable_testing()
add_test(NAME cli_smoke
         COMMAND ${CMAKE_CURRENT_SOURCE_DIR}/tests/cli_smoke.sh
                 ${CMAKE_BINARY_DIR}
         WORKING_DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR})
