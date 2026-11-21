#include "cli.hpp"

int main(int argc, char** argv) {
  return mp4flow::runCli(argc - 1, argv + 1);
}
