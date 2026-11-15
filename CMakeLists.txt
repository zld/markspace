cmake_minimum_required(VERSION 3.16)
project(subtitle_metrics LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

# Core library: starts as INTERFACE (headers only); sources are appended
# per module and it becomes a static library.
add_library(subtitle_core INTERFACE)
target_include_directories(subtitle_core INTERFACE ${CMAKE_CURRENT_SOURCE_DIR}/include)

enable_testing()
