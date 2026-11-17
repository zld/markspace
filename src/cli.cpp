// CLI entry point; subcommands are added in later batches.
#include <cstring>
#include <iostream>
#include <string>

int main(int argc, char **argv) {
  if (argc < 2 || std::strcmp(argv[1], "--help") == 0) {
    std::cout << "usage: fmp4tool <fixture|repack|verify> [options]\n";
    return 0;
  }
  std::cerr << "error: unknown or unimplemented subcommand: " << argv[1]
            << "\n";
  return 2;
}
