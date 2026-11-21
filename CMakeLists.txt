cmake_minimum_required(VERSION 3.28)
project(ass_pipeline LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

add_compile_options(-Wall -Wextra -Wpedantic)

find_package(PkgConfig REQUIRED)
pkg_check_modules(LIBASS REQUIRED IMPORTED_TARGET libass)
find_package(ZLIB REQUIRED)

# Core library; sources are appended per implementation batch.
add_library(asspipe STATIC
  src/style_parser.cpp
)
target_include_directories(asspipe PUBLIC
  ${CMAKE_CURRENT_SOURCE_DIR}/include
)
target_link_libraries(asspipe PUBLIC PkgConfig::LIBASS ZLIB::ZLIB)
