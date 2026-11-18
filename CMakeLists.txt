cmake_minimum_required(VERSION 3.28)

project(lutcheck VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(PROJECT_IS_TOP_LEVEL)
  set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
endif()

find_package(Threads REQUIRED)

add_library(lutcheck_core
  src/lut_parser.cpp
)

target_include_directories(lutcheck_core PUBLIC src)

target_compile_options(lutcheck_core PRIVATE
  -Wall
  -Wextra
  -Wpedantic
  -Wshadow
)

target_link_libraries(lutcheck_core PUBLIC Threads::Threads)

add_executable(lutcheck src/main.cpp)
target_link_libraries(lutcheck PRIVATE lutcheck_core)
target_compile_options(lutcheck PRIVATE -Wall -Wextra -Wpedantic -Wshadow)

