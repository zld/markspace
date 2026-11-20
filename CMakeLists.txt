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
  src/init_compat.cpp
  src/repacker.cpp
  src/fixture.cpp
)
target_include_directories(fmp4core PUBLIC include)

add_executable(fmp4ctl src/main.cpp)
target_link_libraries(fmp4ctl PRIVATE fmp4core)

include(CTest)
if(BUILD_TESTING)
  find_package(Catch2 3 REQUIRED)
  add_executable(fmp4_tests
    tests/index_test.cpp
    tests/repack_test.cpp
  )
  target_link_libraries(fmp4_tests PRIVATE fmp4core Catch2::Catch2WithMain)
  include(Catch)
  catch_discover_tests(fmp4_tests)
endif()
