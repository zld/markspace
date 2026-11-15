cmake_minimum_required(VERSION 3.20)
project(subtitle_audit LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

find_package(PkgConfig REQUIRED)
pkg_check_modules(LIBASS REQUIRED IMPORTED_TARGET libass>=0.17.3)

add_library(assaudit_core STATIC
  src/util.cpp
  src/ass_parser.cpp
)
target_include_directories(assaudit_core PUBLIC include)
target_link_libraries(assaudit_core PUBLIC PkgConfig::LIBASS)

add_executable(subtitle_audit src/main.cpp)
target_link_libraries(subtitle_audit PRIVATE assaudit_core)

enable_testing()
add_executable(fixtures_generator tests/fixtures_generator.cpp)
target_include_directories(fixtures_generator PRIVATE tests include)
add_test(NAME fixtures_generator COMMAND fixtures_generator)

add_executable(integration_tests tests/integration.cpp)
target_include_directories(integration_tests PRIVATE tests include)
target_link_libraries(integration_tests PRIVATE assaudit_core)
add_test(NAME integration_tests COMMAND integration_tests)
