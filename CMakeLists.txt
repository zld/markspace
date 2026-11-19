cmake_minimum_required(VERSION 3.28)
project(h264scan LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
    set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

# Header-only core library: bit reader, exp-golomb, NAL / parameter set /
# slice parsers, access unit assembly and summary writing.
add_library(h264core INTERFACE)
target_include_directories(h264core INTERFACE ${CMAKE_CURRENT_SOURCE_DIR}/src)

option(H264SCAN_BUILD_TESTS "Build Catch2 unit tests" ON)
option(H264SCAN_BUILD_TOOLS "Build fixture generator and scanner executables" ON)

if(H264SCAN_BUILD_TOOLS)
    add_subdirectory(tools)
endif()

if(H264SCAN_BUILD_TESTS)
    enable_testing()
    add_subdirectory(tests)
endif()
