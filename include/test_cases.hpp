#pragma once
//
// test_cases.hpp — Integration test suite entry points.
//
namespace asspipe::test {

void run_parser_tests();      // parsing, inheritance, tag resolution
void run_layout_tests();      // libass rendering, PNG, SHA, limits/exit codes

} // namespace asspipe::test
