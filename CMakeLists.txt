cmake_minimum_required(VERSION 3.27)
project(lutcheck VERSION 1.0.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

find_package(Threads REQUIRED)

add_library(lutcore STATIC
    src/types.cpp
    src/color_space.cpp
    src/cube_parser.cpp
    src/lut_kernel.cpp
    src/render_paths.cpp
    src/histogram.cpp
    src/options.cpp
    src/demo_data.cpp
    src/report.cpp
)
target_include_directories(lutcore PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_compile_options(lutcore PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(lutcore PUBLIC Threads::Threads)

add_executable(lutcheck src/main.cpp)
target_link_libraries(lutcheck PRIVATE lutcore)
target_compile_options(lutcheck PRIVATE -Wall -Wextra -Wpedantic)

include(CTest)
if(BUILD_TESTING)
    add_executable(lutcheck_tests tests/test_pipeline.cpp)
    target_link_libraries(lutcheck_tests PRIVATE lutcore)
    target_compile_options(lutcheck_tests PRIVATE -Wall -Wextra -Wpedantic)
    add_test(NAME pipeline COMMAND lutcheck_tests)
    add_test(NAME cli_demo
             COMMAND lutcheck demo --size 17 --pixels 4096 --threads 4 --format json)
    set_tests_properties(cli_demo PROPERTIES
        PASS_REGULAR_EXPRESSION "\"lut_size\":17")
    add_test(NAME cli_bad_argument COMMAND lutcheck demo --threads 0)
    set_tests_properties(cli_bad_argument PROPERTIES WILL_FAIL TRUE)
    add_test(NAME cli_bad_space COMMAND lutcheck demo --lut-space sepia)
    set_tests_properties(cli_bad_space PROPERTIES WILL_FAIL TRUE)
endif()
