// self_test.cpp - internal consistency checks (stub; full checks land in a
// later commit).
#include <cstdio>

#include "color_types.hpp"

namespace lutcheck {

int run_self_test() {
  std::fprintf(stderr, "self_test=not_implemented\n");
  return 1;
}

}  // namespace lutcheck
