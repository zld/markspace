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
  src/inheritance.cpp
  src/tag_resolver.cpp
  src/fixture_builder.cpp
  src/layout_engine.cpp
  src/layout_event.cpp
  src/sha256.cpp
  src/png_writer.cpp
  src/pipeline.cpp
)
target_include_directories(asspipe PUBLIC
  ${CMAKE_CURRENT_SOURCE_DIR}/include
)
target_link_libraries(asspipe PUBLIC PkgConfig::LIBASS ZLIB::ZLIB)

add_executable(ass_pipeline src/main.cpp)
target_link_libraries(ass_pipeline PRIVATE asspipe)

enable_testing()
add_executable(integration_tests tests/integration.cpp)
target_link_libraries(integration_tests PRIVATE asspipe)
add_test(NAME integration COMMAND integration_tests)
