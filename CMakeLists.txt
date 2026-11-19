cmake_minimum_required(VERSION 3.25)
project(lutcheck VERSION 0.1 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

add_executable(lutcheck
  src/main.cpp
  src/cube_parser.cpp
)

target_include_directories(lutcheck PRIVATE include)
target_compile_options(lutcheck PRIVATE -Wall -Wextra -Wpedantic)
