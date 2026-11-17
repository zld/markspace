cmake_minimum_required(VERSION 3.31)
project(fmp4tool LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

find_package(nlohmann_json 3.11 REQUIRED)

add_library(fmp4 STATIC
  src/box_reader.cpp
)
target_include_directories(fmp4 PUBLIC include)
target_link_libraries(fmp4 PUBLIC nlohmann_json::nlohmann_json)

enable_testing()

add_executable(fmp4tool src/cli.cpp)
target_link_libraries(fmp4tool PRIVATE fmp4)

add_executable(fmp4_tests tests/fmp4_tests.cpp)
target_link_libraries(fmp4_tests PRIVATE fmp4)
add_test(NAME fmp4_tests COMMAND fmp4_tests)
