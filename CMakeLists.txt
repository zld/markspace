cmake_minimum_required(VERSION 3.25)
project(h264scan VERSION 1.0.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(CMAKE_CXX_COMPILER_ID MATCHES "Clang|GNU")
    add_compile_options(-Wall -Wextra -Wpedantic)
endif()

# Core parsing library: bounded bit reader, Annex B scanner, H.264 syntax
# parsing and access-unit grouping.  No third-party H.264 dependencies.
add_library(h264core STATIC
    src/diagnostic.cpp
    src/bit_reader.cpp
    src/nal_scanner.cpp
    src/h264_sps.cpp
    src/h264_slice.cpp
    src/access_unit.cpp
    src/report_json.cpp
    src/report_text.cpp
)
target_include_directories(h264core PUBLIC include)

# Command-line driver for local stream analysis.
add_executable(h264scan src/main.cpp tests/fixture_stream.cpp tests/bit_writer.cpp)
target_include_directories(h264scan PRIVATE include tests)
target_link_libraries(h264scan PRIVATE h264core)

# Deterministic fixture generator; does not link the parsing library so the
# test stream stays an independent encoder-side construction.
add_executable(generate_fixture
    tests/generate_fixture.cpp
    tests/bit_writer.cpp
    tests/fixture_stream.cpp
)
target_include_directories(generate_fixture PRIVATE tests)
