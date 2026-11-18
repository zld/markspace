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
  src/bit_reader.cpp
  src/exp_golomb.cpp
  src/nal_parser.cpp
  src/nal_types.cpp
  src/sps_parser.cpp
  src/sps_dimensions.cpp
  src/pps_parser.cpp
  src/slice_types.cpp
  src/slice_parser.cpp
  src/slice_header_lists.cpp
  src/param_set_store.cpp
)
target_include_directories(h264core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)

enable_testing()

add_executable(h264-self-test
  tests/runner.cpp
  tests/test_bytes.cpp
  tests/test_rbsp.cpp
  tests/test_exp_golomb.cpp
  tests/test_nal.cpp
  tests/test_sps.cpp
  tests/test_slice.cpp
)
target_link_libraries(h264-self-test PRIVATE h264core)
target_include_directories(h264-self-test PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/tests)
add_test(NAME self_test COMMAND h264-self-test)
