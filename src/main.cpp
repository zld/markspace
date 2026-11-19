// main.cpp - command line parsing and pipeline orchestration.
//
// Exit codes: 0 success, 2 argument error, 3 LUT/input format error,
// 4 path-consistency failure, 5 file read/write error.
#include <cstdio>
#include <cstdlib>
#include <string>

#include "color_types.hpp"
#include "cube_parser.hpp"

namespace {

using lutcheck::ColorSpace;
using lutcheck::Lut3D;

constexpr long kMaxPixels = 10000000L;
constexpr double kDeviationTolerance = 1e-6;

struct Options {
  ColorSpace space = ColorSpace::kSrgb;
  int size = 17;
  long pixels = 4096;
  int bins = 16;
  std::size_t block_size = 256;
  bool json = false;
  bool clamp = true;
  std::string lut_path;
  std::string out_path;
};

void print_usage(std::FILE* out) {
  std::fprintf(out,
               "lutcheck - LUT grading consistency checker\n"
               "usage:\n"
               "  lutcheck demo [options]            generate sample LUT+pixels\n"
               "  lutcheck run --lut FILE [options]  grade with a .cube file\n"
               "  lutcheck self-test                 run internal checks\n"
               "options:\n"
               "  --space linear-srgb|srgb|display-p3\n"
               "  --size N        LUT grid size for demo (2..65)\n"
               "  --pixels N      sample pixel count (1..10000000)\n"
               "  --bins N        histogram bins per channel (1..4096)\n"
               "  --block-size N  tiled path block size (>=1)\n"
               "  --json          machine-readable summary\n"
               "  --clamp | --no-clamp  clamp graded output to [0,1]\n"
               "  --out FILE      write summary to file instead of stdout\n");
}

bool parse_long(const std::string& s, long lo, long hi, long& out) {
  if (s.empty()) return false;
  char* end = nullptr;
  const long v = std::strtol(s.c_str(), &end, 10);
  if (end == s.c_str() || *end != '\0' || v < lo || v > hi) return false;
  out = v;
  return true;
}

// Returns false (and prints the reason) on any argument error.
bool parse_args(int argc, char** argv, Options& opt) {
  auto fail = [](const std::string& msg) {
    std::fprintf(stderr, "lutcheck: argument error: %s\n", msg.c_str());
    return false;
  };
  for (int i = 0; i < argc; ++i) {
    std::string arg = argv[i];
    std::string value;
    const std::size_t eq = arg.find('=');
    if (eq != std::string::npos) {
      value = arg.substr(eq + 1);
      arg = arg.substr(0, eq);
    }
    auto take_value = [&](std::string& out) {
      if (eq != std::string::npos) {
        out = value;
        return true;
      }
      if (i + 1 >= argc) return false;
      out = argv[++i];
      return true;
    };
    long n = 0;
    if (arg == "--json") {
      opt.json = true;
    } else if (arg == "--clamp") {
      opt.clamp = true;
    } else if (arg == "--no-clamp") {
      opt.clamp = false;
    } else if (arg == "--space") {
      std::string v;
      if (!take_value(v) || !lutcheck::parse_color_space(v, opt.space)) {
        return fail("--space needs linear-srgb|srgb|display-p3");
      }
    } else if (arg == "--size") {
      std::string v;
      if (!take_value(v) ||
          !parse_long(v, Lut3D::kMinSize, Lut3D::kMaxSize, n)) {
        return fail("--size must be an integer in [2, 65]");
      }
      opt.size = static_cast<int>(n);
    } else if (arg == "--pixels") {
      std::string v;
      if (!take_value(v) || !parse_long(v, 1, kMaxPixels, n)) {
        return fail("--pixels must be an integer in [1, 10000000]");
      }
      opt.pixels = n;
    } else if (arg == "--bins") {
      std::string v;
      if (!take_value(v) || !parse_long(v, 1, 4096, n)) {
        return fail("--bins must be an integer in [1, 4096]");
      }
      opt.bins = static_cast<int>(n);
    } else if (arg == "--block-size") {
      std::string v;
      if (!take_value(v) || !parse_long(v, 1, 1L << 30, n)) {
        return fail("--block-size must be an integer >= 1");
      }
      opt.block_size = static_cast<std::size_t>(n);
    } else if (arg == "--lut") {
      if (!take_value(opt.lut_path)) return fail("--lut needs a path");
    } else if (arg == "--out") {
      if (!take_value(opt.out_path)) return fail("--out needs a path");
    } else {
      return fail("unknown option: " + arg);
    }
  }
  return true;
}

int run_pipeline(const Options& opt, bool demo_mode) {
  (void)opt;
  (void)demo_mode;
  std::fprintf(stderr, "lutcheck: pipeline not linked in this build stage\n");
  return 2;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 2) {
    print_usage(stderr);
    return 2;
  }
  const std::string command = argv[1];
  if (command == "self-test") {
    return lutcheck::run_self_test() == 0 ? 0 : 4;
  }
  if (command == "help" || command == "--help" || command == "-h") {
    print_usage(stdout);
    return 0;
  }
  if (command != "demo" && command != "run") {
    std::fprintf(stderr, "lutcheck: unknown command: %s\n", command.c_str());
    print_usage(stderr);
    return 2;
  }
  Options opt;
  if (!parse_args(argc - 2, argv + 2, opt)) return 2;
  if (command == "demo" && !opt.lut_path.empty()) {
    std::fprintf(stderr, "lutcheck: argument error: --lut conflicts with demo\n");
    return 2;
  }
  if (command == "run" && opt.lut_path.empty()) {
    std::fprintf(stderr, "lutcheck: argument error: run requires --lut FILE\n");
    return 2;
  }
  try {
    return run_pipeline(opt, command == "demo");
  } catch (const lutcheck::ParseError& e) {
    std::fprintf(stderr, "lutcheck: LUT parse error: %s\n", e.what());
    return 3;
  } catch (const lutcheck::IoError& e) {
    std::fprintf(stderr, "lutcheck: io error: %s\n", e.what());
    return 5;
  } catch (const std::bad_alloc&) {
    std::fprintf(stderr, "lutcheck: allocation failed\n");
    return 5;
  } catch (const std::exception& e) {
    std::fprintf(stderr, "lutcheck: internal error: %s\n", e.what());
    return 4;
  }
}
