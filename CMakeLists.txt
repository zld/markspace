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
)
target_include_directories(tl_core PUBLIC include)

add_executable(transition_lab src/main.cpp)
target_link_libraries(transition_lab PRIVATE tl_core)

enable_testing()
