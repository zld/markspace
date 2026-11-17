cmake_minimum_required(VERSION 3.25)
project(transition_lab CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

add_library(tl_core
  src/frame_buffer.cpp
  src/ppm.cpp
  src/transition.cpp
  src/timeline.cpp
  src/biome.cpp
  src/render_pipeline.cpp
)
target_include_directories(tl_core PUBLIC include)

add_executable(transition_lab src/main.cpp)
target_link_libraries(transition_lab PRIVATE tl_core)

enable_testing()

add_executable(renderer_test tests/renderer_test.cpp)
target_link_libraries(renderer_test PRIVATE tl_core)

set(TL_SCRATCH ${CMAKE_CURRENT_BINARY_DIR}/test_scratch)
add_test(NAME endpoints COMMAND renderer_test endpoints)
add_test(NAME determinism COMMAND renderer_test determinism)
add_test(NAME concurrency COMMAND renderer_test concurrency)
add_test(NAME ppm_strict COMMAND renderer_test ppm ${TL_SCRATCH})
add_test(NAME timeline COMMAND renderer_test timeline)
add_test(NAME manifest COMMAND renderer_test manifest ${TL_SCRATCH})
