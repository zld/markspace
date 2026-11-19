find_package(Catch2 3 REQUIRED)

add_executable(h264_tests
    test_bit_reader.cpp
    test_exp_golomb.cpp
    test_nal_parser.cpp
    test_parameter_sets.cpp
    test_access_unit.cpp
    test_integration.cpp
)
target_link_libraries(h264_tests PRIVATE h264core Catch2::Catch2WithMain)
target_include_directories(h264_tests PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/../tools)

# Absolute paths so the integration cases can exec the real binaries.
target_compile_definitions(h264_tests PRIVATE
    H264SCAN_BIN="$<TARGET_FILE:h264scan>"
    FIXTURE_GEN_BIN="$<TARGET_FILE:fixture_gen>"
)
# Integration cases shell out to the executables, so ensure they exist first.
add_dependencies(h264_tests h264scan fixture_gen)

include(CTest)
include(Catch)
catch_discover_tests(h264_tests)
