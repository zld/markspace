cmake_minimum_required(VERSION 3.28)

project(lutctl LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# Header-only foundation: data model and shared declarations.
add_library(lutbase INTERFACE)
target_include_directories(lutbase INTERFACE
    "$<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>")

if(MSVC)
    target_compile_options(lutbase INTERFACE /W4)
else()
    target_compile_options(lutbase INTERFACE -Wall -Wextra -Wpedantic)
endif()

enable_testing()

# Pipeline implementation library; source files are appended in each module.
add_library(lutcore STATIC
    src/model.cpp
)
target_link_libraries(lutcore PUBLIC lutbase)
