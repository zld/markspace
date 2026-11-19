// main.cpp - command line entry point (skeleton; pipeline lands in a later
// commit).
#include <cstdio>

#include "color_types.hpp"
#include "cube_parser.hpp"

namespace {

void print_usage(FILE* out) {
  std::fprintf(out,
               "lutcheck - LUT grading consistency checker\n"
               "usage:\n"
               "  lutcheck demo [options]        generate sample LUT+pixels\n"
               "  lutcheck run --lut FILE [options]  grade with a .cube file\n"
               "  lutcheck self-test             run internal checks\n"
               "options:\n"
               "  --space linear-srgb|srgb|display-p3\n"
               "  --size N        LUT grid size for demo (2..65)\n"
               "  --pixels N      number of sample pixels (1..10000000)\n"
               "  --bins N        histogram bins per channel\n"
               "  --block-size N  tiled path block size (>=1)\n"
               "  --json          machine-readable summary\n"
               "  --clamp / --no-clamp  clamp graded output to [0,1]\n"
               "  --out FILE      write summary to file instead of stdout\n");
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 2) {
    print_usage(stderr);
    return 2;
  }
  print_usage(stdout);
  std::fprintf(stderr, "lutcheck: pipeline not linked in this build stage\n");
  return 2;
}
