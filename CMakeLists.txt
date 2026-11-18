cmake_minimum_required(VERSION 3.28)
project(mp4_repack LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
    set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

add_library(mp4_core STATIC
    src/box_reader.cpp
    src/fragment_index.cpp
    src/fragment_index_io.cpp
    src/compatibility.cpp
    src/rewriter.cpp
    src/rewriter_package.cpp
    src/rewriter_io.cpp
    src/sha256.cpp
)
target_include_directories(mp4_core PUBLIC src)

add_library(fixture_core STATIC
    tools/fixture_builder.cpp
    tools/fixture_builder_frag.cpp
    tools/fixture_builder_fault.cpp
)
target_include_directories(fixture_core PUBLIC tools)
target_link_libraries(fixture_core PUBLIC mp4_core)

add_executable(make_fixtures tools/make_fixtures.cpp)
target_link_libraries(make_fixtures PRIVATE fixture_core)

add_executable(mp4_repack src/main.cpp)
target_link_libraries(mp4_repack PRIVATE mp4_core)

enable_testing()
find_package(Catch2 3.7.1 REQUIRED)

add_executable(mp4_tests
    tests/test_pipeline.cpp
    tests/test_rewriter.cpp
)
target_link_libraries(mp4_tests PRIVATE fixture_core Catch2::Catch2WithMain)

include(CTest)
include(Catch)
catch_discover_tests(mp4_tests)
