cmake_minimum_required(VERSION 3.29)
project(fmp4ctl CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra)

add_library(fmp4core STATIC
  src/box.cpp
  src/fragment_index.cpp
)
target_include_directories(fmp4core PUBLIC include)

add_executable(fmp4ctl src/main.cpp)
target_link_libraries(fmp4ctl PRIVATE fmp4core)
