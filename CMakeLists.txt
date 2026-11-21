cmake_minimum_required(VERSION 3.28)

project(lutctl LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# Header-only foundation: data model and shared declarations.
add_library(lutbase INTERFACE)
target_include_directories(lutbase INTERFACE
    "$<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>")

if(MSVC)
    target_compile_options(lutbase INTERFACE /W4)
else()
    target_compile_options(lutbase INTERFACE -Wall -Wextra -Wpedantic)
endif()

enable_testing()

# Pipeline implementation library; source files are appended in each module.
add_library(lutcore STATIC
    src/model.cpp
    src/color_space.cpp
    src/cube_reader.cpp
    src/io_util.cpp
    src/interpolator.cpp
    src/report.cpp
    src/image.cpp
    src/baker.cpp
    src/render.cpp
    src/fixtures.cpp
)
target_link_libraries(lutcore PUBLIC lutbase)

add_executable(lutctl
    src/cli.cpp
    src/cli_options.cpp
    src/commands.cpp
    src/main.cpp
)
target_link_libraries(lutctl PRIVATE lutcore)

add_executable(test_pipeline
    tests/test_pipeline.cpp
)
target_link_libraries(test_pipeline PRIVATE lutcore)
target_include_directories(test_pipeline PRIVATE tests)
add_test(NAME pipeline COMMAND test_pipeline)

add_executable(test_cli_e2e
    tests/test_cli_e2e.cpp
)
target_link_libraries(test_cli_e2e PRIVATE lutcore)
target_include_directories(test_cli_e2e PRIVATE tests)
add_test(NAME cli_e2e COMMAND test_cli_e2e)
set_tests_properties(cli_e2e PROPERTIES
    ENVIRONMENT "LUTCTL_BIN=$<TARGET_FILE:lutctl>")
