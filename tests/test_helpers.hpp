#ifndef LUT_TEST_HELPERS_HPP
#define LUT_TEST_HELPERS_HPP

#include <cstdio>
#include <cstdlib>
#include <cmath>
#include <filesystem>
#include <string>
#include <unistd.h>

namespace luttest {

inline int& failureCount() {
    static int failures = 0;
    return failures;
}

inline const char*& currentTest() {
    static const char* name = "";
    return name;
}

inline void beginTest(const char* name) {
    currentTest() = name;
}

inline void recordFailure(const char* expr, const char* file, int line) {
    ++failureCount();
    std::fprintf(stderr, "FAIL [%s] %s:%d: %s\n", currentTest(), file, line,
                 expr);
}

inline int finish() {
    if (failureCount() == 0) {
        std::puts("ALL TESTS PASSED");
        return 0;
    }
    std::fprintf(stderr, "%d CHECK(S) FAILED\n", failureCount());
    return 1;
}

// Unique scratch directory per test process, removed on creation.
inline std::string scratchDir() {
    auto path = std::filesystem::temp_directory_path() /
                ("lut-test-" + std::to_string(::getpid()));
    std::filesystem::remove_all(path);
    std::filesystem::create_directories(path);
    return path.string();
}

}  // namespace luttest

#define CHECK(expr)                                                    \
    do {                                                               \
        if (!(expr)) {                                                 \
            ::luttest::recordFailure(#expr, __FILE__, __LINE__);      \
        }                                                              \
    } while (0)

#define CHECK_NEAR(a, b, eps)                                                    \
    do {                                                                         \
        const double va_ = (a);                                                  \
        const double vb_ = (b);                                                  \
        if (std::fabs(va_ - vb_) > (eps)) {                                     \
            ::luttest::recordFailure(#a " ~= " #b, __FILE__, __LINE__);         \
        }                                                                        \
    } while (0)

#define RUN_TEST(name)                       \
    do {                                     \
        ::luttest::beginTest(#name);        \
        name();                              \
    } while (0)

#endif  // LUT_TEST_HELPERS_HPP
