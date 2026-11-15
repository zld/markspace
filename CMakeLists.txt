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
  src/group_index.cpp
  src/fixture_writer.cpp
)
target_include_directories(h264_core PUBLIC include)

add_executable(h264_inspect src/main.cpp)
target_link_libraries(h264_inspect PRIVATE h264_core)

# Vendored Catch2 3.5.4 amalgamated build; fully offline.
add_library(catch2_amalg STATIC tests/third_party/catch2/catch_amalgamated.cpp)
target_include_directories(catch2_amalg PUBLIC tests/third_party/catch2)
if(NOT MSVC)
  target_compile_options(catch2_amalg PRIVATE -Wno-unused-parameter)
endif()

enable_testing()
add_executable(h264_tests
  tests/generate_fixture.cpp
  tests/parser_tests.cpp
)
target_link_libraries(h264_tests PRIVATE h264_core catch2_amalg)
add_test(NAME h264_tests COMMAND h264_tests)
