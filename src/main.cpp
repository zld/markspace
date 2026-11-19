// main.cpp - command line parsing and pipeline orchestration.
//
// Exit codes: 0 success, 2 argument error, 3 LUT/input format error,
// 4 path-consistency failure, 5 file read/write error.
#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <iostream>
#include <new>
#include <ostream>
#include <string>
#include <vector>

#include "color_types.hpp"
#include "cube_parser.hpp"

namespace {

using lutcheck::ColorSpace;
using lutcheck::HistogramSet;
using lutcheck::Lut3D;
using lutcheck::Rgb;

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

double fract(double v) { return v - std::floor(v); }

// Deterministic sample pixels authored as sRGB-encoded values: color bars
// first, then a gradient from irrational strides. Out-of-range boundary
// samples are injected later, after the working-space conversion.
std::vector<Rgb> generate_pixels(std::size_t count) {
  static const Rgb kBars[8] = {{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1},
                               {1, 1, 0}, {1, 0, 1}, {0, 1, 1}, {1, 1, 1}};
  std::vector<Rgb> px;
  px.reserve(count);
  for (std::size_t i = 0; i < count; ++i) {
    if (i < 8) {
      px.push_back(kBars[i]);
    } else {
      const double t = static_cast<double>(i);
      px.push_back(Rgb{static_cast<float>(fract(t * 0.7548776662466927)),
                       static_cast<float>(fract(t * 0.5698402909980532)),
                       static_cast<float>(fract(t * 0.3183098861837907))});
    }
  }
  return px;
}

// Overwrite a few leading pixels with boundary and out-of-range values in
// the working space, so clamping and histogram under/overflow are exercised
// regardless of the selected color space.
void inject_boundary_samples(std::vector<Rgb>& px) {
  static const Rgb kBounds[6] = {{0, 0, 0},
                                 {1, 1, 1},
                                 {-0.05f, 0.5f, 0.5f},
                                 {1.05f, 0.5f, 0.5f},
                                 {0.5f, -0.02f, 1.03f},
                                 {0.5f, 1.07f, -0.01f}};
  for (std::size_t i = 0; i < 6 && 8 + i < px.size(); ++i) {
    px[8 + i] = kBounds[i];
  }
}

// Re-encode generated sRGB pixels into the requested working space,
// converting through XYZ in the linear-light domain.
void convert_pixels_to_space(std::vector<Rgb>& px, ColorSpace target) {
  if (target == ColorSpace::kSrgb) return;
  const lutcheck::Mat3 to_xyz = lutcheck::rgb_to_xyz_matrix(ColorSpace::kSrgb);
  const lutcheck::Mat3 from_xyz = lutcheck::xyz_to_rgb_matrix(target);
  for (Rgb& p : px) {
    const Rgb lin = lutcheck::space_decode(ColorSpace::kSrgb, p);
    if (target == ColorSpace::kLinearSrgb) {
      p = lin;
    } else {
      p = lutcheck::space_encode(
          target,
          lutcheck::mat_apply(from_xyz, lutcheck::mat_apply(to_xyz, lin)));
    }
  }
}

// Deterministic synthetic grade: mild cross-channel mix plus a smoothstep
// toe on blue, sampled onto the grid in the [0,1] domain.
Lut3D make_synthetic_lut(int size) {
  Lut3D lut;
  lut.size = size;
  lut.title = "synthetic-demo";
  lut.data.resize(static_cast<std::size_t>(size) * size * size * 3u);
  for (int r = 0; r < size; ++r) {
    const double x = static_cast<double>(r) / (size - 1);
    for (int g = 0; g < size; ++g) {
      const double y = static_cast<double>(g) / (size - 1);
      for (int b = 0; b < size; ++b) {
        const double z = static_cast<double>(b) / (size - 1);
        const std::size_t idx = lut.node_index(r, g, b);
        lut.data[idx + 0] = static_cast<float>(0.85 * x + 0.10 * y + 0.02);
        lut.data[idx + 1] = static_cast<float>(0.05 * x + 0.88 * y + 0.04 * z);
        lut.data[idx + 2] =
            static_cast<float>(0.01 + 0.94 * z * z * (3.0 - 2.0 * z));
      }
    }
  }
  return lut;
}

void print_histogram_text(std::ostream& os, const char* tag,
                          const HistogramSet& hs) {
  const char* names[3] = {"r", "g", "b"};
  for (int c = 0; c < 3; ++c) {
    os << tag << " channel=" << names[c]
       << " underflow=" << hs.channel[c].underflow
       << " overflow=" << hs.channel[c].overflow << " bins=";
    for (std::size_t i = 0; i < hs.channel[c].bins.size(); ++i) {
      os << (i ? "," : "") << hs.channel[c].bins[i];
    }
    os << "\n";
  }
}

void print_histogram_json(std::ostream& os, const HistogramSet& hs) {
  const char* names[3] = {"r", "g", "b"};
  os << "{";
  for (int c = 0; c < 3; ++c) {
    os << (c ? "," : "") << "\"" << names[c] << "\":{\"underflow\":"
       << hs.channel[c].underflow << ",\"overflow\":" << hs.channel[c].overflow
       << ",\"bins\":[";
    for (std::size_t i = 0; i < hs.channel[c].bins.size(); ++i) {
      os << (i ? "," : "") << hs.channel[c].bins[i];
    }
    os << "]}";
  }
  os << "}";
}

int run_pipeline(const Options& opt, bool demo_mode) {
  Lut3D lut = demo_mode ? make_synthetic_lut(opt.size)
                        : lutcheck::parse_cube_file(opt.lut_path);
  const std::size_t count = static_cast<std::size_t>(opt.pixels);
  if (count > static_cast<std::size_t>(-1) / sizeof(Rgb)) {
    std::fprintf(stderr, "lutcheck: pixel count overflows allocation size\n");
    return 2;
  }
  std::vector<Rgb> pixels = generate_pixels(count);
  convert_pixels_to_space(pixels, opt.space);
  inject_boundary_samples(pixels);

  lutcheck::RenderOptions ropt;
  ropt.space = opt.space;
  ropt.clamp_output = opt.clamp;

  const HistogramSet before = lutcheck::compute_histogram(pixels, opt.bins);
  std::vector<Rgb> out_scalar, out_tiled;
  lutcheck::render_scalar(lut, pixels, out_scalar, ropt);
  lutcheck::render_tiled(lut, pixels, out_tiled, opt.block_size, ropt);
  const HistogramSet after = lutcheck::compute_histogram(out_tiled, opt.bins);

  double dev[3] = {0.0, 0.0, 0.0};
  for (std::size_t i = 0; i < count; ++i) {
    const float a[3] = {out_scalar[i].r, out_scalar[i].g, out_scalar[i].b};
    const float b[3] = {out_tiled[i].r, out_tiled[i].g, out_tiled[i].b};
    for (int c = 0; c < 3; ++c) {
      const double d = std::fabs(static_cast<double>(a[c]) - b[c]);
      if (d > dev[c]) dev[c] = d;
    }
  }

  std::ofstream file;
  std::ostream& os = opt.out_path.empty() ? std::cout : file;
  if (!opt.out_path.empty()) {
    file.open(opt.out_path, std::ios::binary | std::ios::trunc);
    if (!file) throw lutcheck::IoError("cannot write summary: " + opt.out_path);
  }
  char grid[32];
  std::snprintf(grid, sizeof grid, "%dx%dx%d", lut.size, lut.size, lut.size);
  if (opt.json) {
    char num[3][32];
    for (int c = 0; c < 3; ++c) std::snprintf(num[c], 32, "%.9e", dev[c]);
    os << "{\n  \"lut_grid\": \"" << grid << "\",\n  \"space\": \""
       << lutcheck::color_space_name(opt.space) << "\",\n"
       << "  \"input_pixels\": " << count << ",\n  \"output_pixels\": " << count
       << ",\n  \"max_abs_deviation_rgb\": [" << num[0] << ", " << num[1]
       << ", " << num[2] << "],\n  \"histogram_before\": ";
    print_histogram_json(os, before);
    os << ",\n  \"histogram_after\": ";
    print_histogram_json(os, after);
    os << "\n}\n";
  } else {
    os << "lut_grid=" << grid << "\n"
       << "space=" << lutcheck::color_space_name(opt.space) << "\n"
       << "input_pixels=" << count << "\n"
       << "output_pixels=" << count << "\n";
    char line[160];
    std::snprintf(line, sizeof line, "max_abs_deviation_rgb=%.9e %.9e %.9e\n",
                  dev[0], dev[1], dev[2]);
    os << line;
    print_histogram_text(os, "histogram_before", before);
    print_histogram_text(os, "histogram_after", after);
  }
  os.flush();
  if (!os) throw lutcheck::IoError("failed while writing summary");

  if (dev[0] >= kDeviationTolerance || dev[1] >= kDeviationTolerance ||
      dev[2] >= kDeviationTolerance) {
    std::fprintf(stderr, "lutcheck: scalar/tiled paths diverge beyond %.0e\n",
                 kDeviationTolerance);
    return 4;
  }
  return 0;
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
