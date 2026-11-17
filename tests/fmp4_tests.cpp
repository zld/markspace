// Test harness; parser/index/validator tests are added in later batches.
#include <cstdio>
#include <exception>
#include <functional>
#include <vector>

static int g_failures = 0;
static std::vector<std::function<void()>> g_tests;

#define FMP4_TEST(name)                                                      \
  static void name();                                                        \
  namespace {                                                                \
  struct Reg_##name {                                                        \
    Reg_##name() { g_tests.push_back(name); }                                \
  } reg_##name;                                                              \
  }                                                                          \
  static void name()

#define FMP4_CHECK(cond)                                                     \
  do {                                                                       \
    if (!(cond)) {                                                           \
      std::printf("FAIL %s:%d %s\n", __FILE__, __LINE__, #cond);             \
      ++g_failures;                                                          \
    }                                                                        \
  } while (0)

int main() {
  for (auto &t : g_tests) {
    try {
      t();
    } catch (const std::exception &e) {
      std::printf("FAIL uncaught exception: %s\n", e.what());
      ++g_failures;
    }
  }
  std::printf("%zu tests, %d failures\n", g_tests.size(), g_failures);
  return g_failures ? 1 : 0;
}
