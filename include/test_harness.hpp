#pragma once
//
// test_harness.hpp — Minimal assertion framework for the integration
// tests. No external test framework is needed; failures print the location
// and a message and flip a global flag checked by main().
//
#include <cstdio>
#include <string>

namespace asspipe::test {

inline int& failure_count() {
    static int n = 0;
    return n;
}

inline int& check_count() {
    static int n = 0;
    return n;
}

template <typename T>
void check(bool cond, const char* expr, const char* file, int line,
           const T& detail) {
    ++check_count();
    if (!cond) {
        ++failure_count();
        std::printf("  FAIL %s:%d: %s (%s)\n", file, line, expr,
                    std::to_string(detail).c_str());
    }
}

inline void check_str(bool cond, const char* expr, const char* file, int line,
                      const std::string& detail) {
    ++check_count();
    if (!cond) {
        ++failure_count();
        std::printf("  FAIL %s:%d: %s (%s)\n", file, line, expr,
                    detail.c_str());
    }
}

} // namespace asspipe::test

#define CHECK(cond, detail)                                                  \
    ::asspipe::test::check(static_cast<bool>(cond), #cond, __FILE__,        \
                           __LINE__, detail)
#define CHECK_STR(cond, detail)                                              \
    ::asspipe::test::check_str(static_cast<bool>(cond), #cond, __FILE__,    \
                               __LINE__, detail)
