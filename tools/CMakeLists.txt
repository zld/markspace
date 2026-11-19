# Command line scanner. The fixture generator target is added separately.
add_executable(h264scan ${CMAKE_CURRENT_SOURCE_DIR}/../src/main.cpp)
target_link_libraries(h264scan PRIVATE h264core)

# Deterministic sample-stream generator; shares the parser headers via h264core.
add_executable(fixture_gen generate_fixture.cpp)
target_link_libraries(fixture_gen PRIVATE h264core)
target_include_directories(fixture_gen PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
