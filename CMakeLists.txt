cmake_minimum_required(VERSION 3.27)
project(transition_render LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_library(tr_core STATIC
  src/frame_buffer.cpp
  src/hash.cpp
  src/timeline.cpp
)
target_include_directories(tr_core PUBLIC src)
target_compile_options(tr_core PRIVATE -Wall -Wextra -Wpedantic)
