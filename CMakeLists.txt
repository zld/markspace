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
  src/color_space.cpp
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


include(CTest)
option(LUTCHECK_BUILD_TESTS "Build the Catch2 test suite" ${BUILD_TESTING})

if(LUTCHECK_BUILD_TESTS)
  find_package(Catch2 3.5 QUIET)
  if(NOT Catch2_FOUND)
    include(FetchContent)
    FetchContent_Declare(
      Catch2
      GIT_REPOSITORY https://github.com/catchorg/Catch2.git
      GIT_TAG v3.5.4
      GIT_SHALLOW TRUE
    )
    FetchContent_MakeAvailable(Catch2)
  endif()

  add_executable(lutcheck_tests tests/test_pipeline.cpp)
  target_link_libraries(lutcheck_tests PRIVATE lutcheck_core Catch2::Catch2WithMain)
  target_compile_options(lutcheck_tests PRIVATE -Wall -Wextra -Wpedantic -Wshadow)

  include(Catch)
  catch_discover_tests(lutcheck_tests)
endif()
