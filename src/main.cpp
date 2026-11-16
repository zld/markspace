#include <cstdio>
#include <cstring>

#include "avsync/common.hpp"

// Placeholder entry point; pipeline wiring lands in later module batches.
int main(int argc, char** argv) {
  if (argc < 2 || std::strcmp(argv[1], "run") != 0) {
    std::fprintf(stderr,
                 "usage: avsync run --input <file> [--jitter-ms N] [--seed N]\n"
                 "                    [--realtime-factor X] [--json out.json]\n");
    return static_cast<int>(avsync::ExitCode::UsageOrInput);
  }
  std::fprintf(stderr, "avsync: pipeline not yet implemented\n");
  return static_cast<int>(avsync::ExitCode::UsageOrInput);
}
