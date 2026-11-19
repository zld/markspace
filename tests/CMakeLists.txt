find_package(Catch2 3 REQUIRED)

add_executable(h264_tests
    test_bit_reader.cpp
    test_exp_golomb.cpp
    test_nal_parser.cpp
    test_parameter_sets.cpp
    test_access_unit.cpp
)
target_link_libraries(h264_tests PRIVATE h264core Catch2::Catch2WithMain)
target_include_directories(h264_tests PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/../tools)

include(CTest)
include(Catch)
catch_discover_tests(h264_tests)
