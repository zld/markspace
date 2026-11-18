// Minimal assertion harness shared by all self-test batches.
#pragma once

#include <cstdio>
#include <cstdlib>
#include <string>

namespace selftest {

inline int& failure_count() {
    static int count = 0;
    return count;
}

inline void record_failure(const char* file, int line, const std::string& what) {
    ++failure_count();
    std::fprintf(stderr, "FAIL %s:%d: %s\n", file, line, what.c_str());
}

}  // namespace selftest

#define CHECK(cond)                                                       \
    do {                                                                  \
        if (!(cond)) {                                                    \
            ::selftest::record_failure(__FILE__, __LINE__, #cond);        \
        }                                                                 \
    } while (0)

#define CHECK_EQ(actual, expected)                                            \
    do {                                                                      \
        const auto a_ = (actual);                                             \
        const auto e_ = (expected);                                           \
        if (!(a_ == e_)) {                                                    \
            ::selftest::record_failure(                                       \
                __FILE__, __LINE__,                                           \
                std::string(#actual) + " == " + #expected + " (got " +        \
                    std::to_string(a_) + ", want " + std::to_string(e_) + ")"); \
        }                                                                     \
    } while (0)

#define CHECK_GT(actual, floor)                                            \
    do {                                                                   \
        const auto a_ = (actual);                                          \
        const auto f_ = (floor);                                           \
        if (!(a_ > f_)) {                                                  \
            ::selftest::record_failure(                                    \
                __FILE__, __LINE__,                                        \
                std::string(#actual) + " > " + #floor + " (got " +         \
                    std::to_string(a_) + ")");                             \
        }                                                                  \
    } while (0)
