cmake_minimum_required(VERSION 3.25)
project(transition_lab LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

add_library(tl_core STATIC
  src/hash.cpp
  src/manifest.cpp
)
target_include_directories(tl_core PUBLIC include)

add_executable(transition_lab src/main.cpp)
target_link_libraries(transition_lab PRIVATE tl_core)

if(CMAKE_CXX_COMPILER_ID MATCHES "Clang|GNU")
  target_compile_options(tl_core PRIVATE -Wall -Wextra -Wpedantic)
  target_compile_options(transition_lab PRIVATE -Wall -Wextra -Wpedantic)
endif()
