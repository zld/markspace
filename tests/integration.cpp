#include <cstdio>

#include "test_cases.hpp"
#include "test_harness.hpp"

int main() {
    std::puts("== ASS pipeline integration tests ==");
    asspipe::test::run_parser_tests();
    asspipe::test::run_layout_tests();

    std::printf("\nchecks=%d failures=%d\n", asspipe::test::check_count(),
                asspipe::test::failure_count());
    return asspipe::test::failure_count() == 0 ? 0 : 1;
}
