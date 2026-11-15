cmake_minimum_required(VERSION 3.20)
project(h264_inspect LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

if(MSVC)
  add_compile_options(/W4)
else()
  add_compile_options(-Wall -Wextra -Wpedantic)
endif()

# Grows module by module; every revision builds the sources present so far.
add_library(h264_core STATIC
  src/bit_reader.cpp
  src/exp_golomb.cpp
  src/nal_scanner.cpp
  src/h264_parser.cpp
)
target_include_directories(h264_core PUBLIC include)
