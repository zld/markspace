cmake_minimum_required(VERSION 3.25)

project(h264-inspect LANGUAGES CXX VERSION 1.0.0)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(CMAKE_CXX_COMPILER_ID MATCHES "Clang|GNU")
  add_compile_options(-Wall -Wextra -Wpedantic -Wshadow -Wnon-virtual-dtor)
endif()

add_library(h264core STATIC
  src/byte_source.cpp
)
target_include_directories(h264core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)

