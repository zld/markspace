cmake_minimum_required(VERSION 3.28)
project(h264scan LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_library(h264core STATIC
  src/bit_reader.cpp
  src/bit_writer.cpp
  src/annexb_reader.cpp
  src/syntax_parser.cpp
  src/au_assembler.cpp
  src/gop_index.cpp
  src/stats.cpp
  src/fixture_writer.cpp
  src/analyzer.cpp
  src/report.cpp
  src/self_test.cpp
  src/self_test.cpp
)
target_include_directories(h264core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)

if(CMAKE_CXX_COMPILER_ID MATCHES "Clang|GNU")
  target_compile_options(h264core PRIVATE -Wall -Wextra -Wpedantic)
endif()

add_executable(h264scan src/main.cpp)
target_link_libraries(h264scan PRIVATE h264core)

if(CMAKE_CXX_COMPILER_ID MATCHES "Clang|GNU")
  target_compile_options(h264scan PRIVATE -Wall -Wextra -Wpedantic)
endif()

enable_testing()
add_test(NAME h264scan_self_test COMMAND h264scan self-test)
