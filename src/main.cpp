#include <cstdio>

// Skeleton entry point; subcommands are added in a later batch.
int main() {
  std::fprintf(stderr,
               "usage: transition_lab make-demo --out PATH\n"
               "       transition_lab render --manifest PATH --out DIR [--verify]\n");
  return 2;
}
