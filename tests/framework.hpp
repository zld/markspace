#ifndef GRADE_TESTS_FRAMEWORK_HPP
#define GRADE_TESTS_FRAMEWORK_HPP

#include <cmath>
#include <cstdio>
#include <functional>
#include <string>
#include <vector>

namespace gradetest {

struct TestCase {
    std::string name;
    std::function<void()> body;
};

inline std::vector<TestCase>& registry() {
    static std::vector<TestCase> cases;
    return cases;
}

struct Registrar {
    Registrar(const std::string& name, std::function<void()> body) {
        registry().push_back({name, std::move(body)});
    }
};

inline int runAll() {
    int failures = 0;
    for (const auto& test : registry()) {
        try {
            test.body();
            std::printf("[PASS] %s\n", test.name.c_str());
        } catch (const std::exception& error) {
            std::printf("[FAIL] %s: %s\n", test.name.c_str(), error.what());
            ++failures;
        } catch (...) {
            std::printf("[FAIL] %s: unknown exception\n", test.name.c_str());
            ++failures;
        }
    }
    std::printf("tests=%zu failures=%d\n", registry().size(), failures);
    return failures == 0 ? 0 : 1;
}

} // namespace gradetest

#define GRADE_TEST(name)                                                    \
    static void name##_body();                                              \
    static ::gradetest::Registrar name##_registrar(#name, name##_body);     \
    static void name##_body()

#define GRADE_REQUIRE(condition)                                            \
    do {                                                                    \
        if (!(condition)) {                                                 \
            throw std::runtime_error(std::string("require failed: ") +      \
                                     #condition + " (" __FILE__ ":" +       \
                                     std::to_string(__LINE__) + ")");       \
        }                                                                   \
    } while (0)

#define GRADE_REQUIRE_NEAR(actual, expected, epsilon)                       \
    do {                                                                    \
        double grade_diff = std::fabs(static_cast<double>(actual) -         \
                                      static_cast<double>(expected));       \
        if (!(grade_diff <= (epsilon))) {                                   \
            throw std::runtime_error(                                       \
                std::string("require near failed: ") + #actual + "=" +      \
                std::to_string(static_cast<double>(actual)) + " expected " +\
                std::to_string(static_cast<double>(expected)) +             \
                " (" __FILE__ ":" + std::to_string(__LINE__) + ")");        \
        }                                                                   \
    } while (0)

#define GRADE_REQUIRE_THROWS(expression, error_type)                        \
    do {                                                                    \
        bool grade_threw = false;                                           \
        try {                                                               \
            expression;                                                     \
        } catch (const error_type&) {                                       \
            grade_threw = true;                                             \
        }                                                                   \
        GRADE_REQUIRE(grade_threw);                                         \
    } while (0)

#endif
