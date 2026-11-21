cmake_minimum_required(VERSION 3.29)
project(mp4flow LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra -Wpedantic -Wshadow -Wconversion)

# Core static library: parser, indexer, repacker, validator.
add_library(mp4flow_core STATIC
  src/box_reader.cpp
  src/init_compat.cpp
  src/box_writer.cpp
  src/fragment_index.cpp
  src/fixture_builder.cpp
  src/repack.cpp
  src/validator.cpp
)
target_include_directories(mp4flow_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)

option(MP4FLOW_BUILD_TESTS "Build Catch2 based tests" ON)

add_executable(mp4flow src/main.cpp src/cli.cpp)
target_link_libraries(mp4flow PRIVATE mp4flow_core)
