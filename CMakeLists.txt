cmake_minimum_required(VERSION 3.29)
project(lutgrade LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

add_compile_options(-Wall -Wextra -Wpedantic -Wshadow -Wconversion)

add_library(grade
  src/cli.cpp
  src/color.cpp
  src/cube_lut.cpp
  src/fixture.cpp
  src/image_ppm.cpp
  src/render.cpp
)
target_include_directories(grade PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
find_package(Threads REQUIRED)
target_link_libraries(grade PUBLIC Threads::Threads)

add_executable(lutgrade src/main.cpp)
target_link_libraries(lutgrade PRIVATE grade)

enable_testing()
add_executable(test_pipeline tests/test_pipeline.cpp)
target_link_libraries(test_pipeline PRIVATE grade)
add_test(NAME pipeline COMMAND test_pipeline)
