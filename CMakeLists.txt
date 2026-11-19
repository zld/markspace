cmake_minimum_required(VERSION 3.27)
project(lutcheck VERSION 1.0.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

find_package(Threads REQUIRED)

add_library(lutcore STATIC
    src/types.cpp
    src/color_space.cpp
    src/cube_parser.cpp
    src/lut_kernel.cpp
)
target_include_directories(lutcore PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_compile_options(lutcore PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(lutcore PUBLIC Threads::Threads)

include(CTest)
