// Minimal test registry so suites compile into one runner.
#pragma once

#include <cstdio>
#include <string>
#include <vector>

#include "test_harness.hpp"

namespace selftest {

struct TestCase {
    const char* name;
    void (*fn)();
};

inline std::vector<TestCase>& registry() {
    static std::vector<TestCase> tests;
    return tests;
}

struct Registrar {
    Registrar(const char* name, void (*fn)()) { registry().push_back({name, fn}); }
};

inline int run_all() {
    for (const TestCase& test : registry()) {
        std::fprintf(stderr, "[ RUN  ] %s\n", test.name);
        test.fn();
    }
    const int failures = failure_count();
    if (failures != 0) {
        std::fprintf(stderr, "%d check(s) failed\n", failures);
        return 1;
    }
    std::fprintf(stderr, "all %zu test(s) passed\n", registry().size());
    return 0;
}

}  // namespace selftest

#define REGISTER_TEST(fn)                                            \
    namespace {                                                      \
    const ::selftest::Registrar registrar_##fn(#fn, &fn);            \
    }
