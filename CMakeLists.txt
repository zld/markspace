cmake_minimum_required(VERSION 3.24)
project(subtitle_probe LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
    set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

# Homebrew installs pkg-config outside the default PATH on some macOS setups.
find_program(PKG_CONFIG_EXECUTABLE NAMES pkg-config
    HINTS /opt/homebrew/bin /usr/local/bin)
if(NOT PKG_CONFIG_EXECUTABLE)
    message(FATAL_ERROR "pkg-config not found; run: brew install cmake pkg-config libass nlohmann-json")
endif()

find_package(PkgConfig REQUIRED)
find_package(Threads REQUIRED)

pkg_check_modules(LIBASS REQUIRED IMPORTED_TARGET libass>=0.17)
pkg_check_modules(NLOHMANN_JSON REQUIRED IMPORTED_TARGET nlohmann_json>=3.11)

add_library(subtitle_core STATIC
    src/ass_document.cpp
    src/style_resolver.cpp
    src/inline_tags.cpp
    src/layout_probe.cpp
    src/report.cpp
)
target_include_directories(subtitle_core PUBLIC src)
target_link_libraries(subtitle_core PUBLIC
    PkgConfig::LIBASS
    PkgConfig::NLOHMANN_JSON
    Threads::Threads
)
target_compile_options(subtitle_core PRIVATE -Wall -Wextra -Wpedantic)

add_executable(subtitle_probe src/main.cpp)
target_link_libraries(subtitle_probe PRIVATE subtitle_core)
target_compile_options(subtitle_probe PRIVATE -Wall -Wextra -Wpedantic)

add_executable(subtitle_probe_tests tests/integration.cpp)
target_link_libraries(subtitle_probe_tests PRIVATE subtitle_core)
target_compile_options(subtitle_probe_tests PRIVATE -Wall -Wextra -Wpedantic)

enable_testing()
add_test(NAME integration COMMAND subtitle_probe_tests)
