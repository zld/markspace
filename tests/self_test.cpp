// self_test.cpp - internal consistency checks for the parser, matrices,
// transfer functions, interpolation kernel and render-path agreement.
#include <cmath>
#include <cstdio>
#include <stdexcept>
#include <string>
#include <vector>

#include "color_types.hpp"
#include "cube_parser.hpp"

namespace lutcheck {
namespace {

int g_failures = 0;

void check(bool ok, const char* name) {
  if (!ok) {
    ++g_failures;
    std::fprintf(stderr, "self_test: FAIL %s\n", name);
  }
}

bool near(double a, double b, double tol) { return std::fabs(a - b) <= tol; }

bool near_rgb(const Rgb& a, const Rgb& b, double tol) {
  return near(a.r, b.r, tol) && near(a.g, b.g, tol) && near(a.b, b.b, tol);
}

// Expect parse_cube_text to throw a ParseError whose reported line matches
// (line 0 means "not tied to a line").
void expect_parse_error(const std::string& text, int line, const char* name) {
  try {
    parse_cube_text(text);
  } catch (const ParseError& e) {
    check(e.line() == line, name);
    if (e.line() != line) {
      std::fprintf(stderr, "  expected line %d, got %d (%s)\n", line, e.line(),
                   e.what());
    }
    return;
  }
  check(false, name);  // no error raised
}

std::string identity_cube(int size) {
  std::string text = "TITLE \"identity\"\nLUT_3D_SIZE " + std::to_string(size) +
                     "\nDOMAIN_MIN 0 0 0\nDOMAIN_MAX 1 1 1\n";
  for (int r = 0; r < size; ++r) {
    for (int g = 0; g < size; ++g) {
      for (int b = 0; b < size; ++b) {
        char row[96];
        std::snprintf(row, sizeof row, "%g %g %g\n",
                      static_cast<double>(r) / (size - 1),
                      static_cast<double>(g) / (size - 1),
                      static_cast<double>(b) / (size - 1));
        text += row;
      }
    }
  }
  return text;
}

void test_parser() {
  // Valid minimal file; rows are ordered blue-fastest, then green, then red.
  const std::string good =
      "TITLE \"t\" # comment\nLUT_3D_SIZE 2\n"
      "0 0 0\n0 0 1\n0 1 0\n0 1 1\n1 0 0\n1 0 1\n1 1 0\n1 1 1\n";
  Lut3D lut = parse_cube_text(good);
  check(lut.size == 2 && lut.title == "\"t\"", "parse.basic");
  check(lut.data[lut.node_index(0, 0, 1) + 2] == 1.0f, "parse.blue_fastest");
  check(lut.data[lut.node_index(1, 0, 0) + 0] == 1.0f, "parse.red_slowest");
  check(near(lut.domain_min[0], 0.0, 0) && near(lut.domain_max[0], 1.0, 0),
        "parse.domain_default");

  expect_parse_error("TITLE \"only\"\n", 0, "parse.missing_size");
  expect_parse_error("LUT_3D_SIZE 2\nLUT_3D_SIZE 2\n", 2, "parse.dup_size");
  expect_parse_error("TITLE a\nTITLE b\nLUT_3D_SIZE 2\n", 2, "parse.dup_title");
  expect_parse_error("LUT_3D_SIZE 1\n", 1, "parse.size_low");
  expect_parse_error("LUT_3D_SIZE 66\n", 1, "parse.size_high");
  expect_parse_error("LUT_3D_SIZE 2.5\n", 1, "parse.size_fractional");
  expect_parse_error("LUT_3D_SIZE 2\n0 0 nan\n", 2, "parse.nan");
  expect_parse_error("LUT_3D_SIZE 2\n0 inf 0\n", 2, "parse.inf");
  expect_parse_error("LUT_3D_SIZE 2\n0 0 0 0\n", 2, "parse.row_width");
  expect_parse_error("LUT_3D_SIZE 2\n0 0 0\n", 0, "parse.too_few_rows");
  {
    std::string extra = good + "0 0 0\n";
    expect_parse_error(extra, 11, "parse.too_many_rows");
  }
  expect_parse_error("LUT_3D_SIZE 2\nDOMAIN_MIN 0.5 0 0\nDOMAIN_MAX 0.5 1 1\n",
                     3, "parse.domain_equal");
  expect_parse_error("LUT_3D_SIZE 2\nDOMAIN_MIN 0.9 0 0\nDOMAIN_MAX 0.1 1 1\n",
                     3, "parse.domain_inverted");
  expect_parse_error("LUT_1D_SIZE 16\n", 1, "parse.lut1d_rejected");
  expect_parse_error("GIBBERISH 1 2 3\n", 1, "parse.unknown_keyword");
  expect_parse_error("0 0 0\nLUT_3D_SIZE 2\n", 1, "parse.data_before_size");
}

void test_color_space() {
  const Mat3 fwd = rgb_to_xyz_matrix(ColorSpace::kSrgb);
  const Mat3 inv = xyz_to_rgb_matrix(ColorSpace::kSrgb);
  const Mat3 prod = mat_mul(inv, fwd);
  bool identity = true;
  for (int r = 0; r < 3; ++r) {
    for (int c = 0; c < 3; ++c) {
      identity &= near(prod.m[r][c], r == c ? 1.0 : 0.0, 1e-12);
    }
  }
  check(identity, "matrix.roundtrip");

  // Linear sRGB white maps to the D65 white point.
  const Rgb white = mat_apply(fwd, Rgb{1, 1, 1});
  check(near(white.r, 0.9505, 1e-3) && near(white.g, 1.0, 1e-9) &&
            near(white.b, 1.0890, 1e-3),
        "matrix.white_point");

  for (double v : {0.0, 0.25, 0.5, 0.75, 1.0, -0.2, 1.3}) {
    const Rgb in{static_cast<float>(v), static_cast<float>(v),
                 static_cast<float>(v)};
    const Rgb rt = space_encode(ColorSpace::kSrgb,
                                space_decode(ColorSpace::kSrgb, in));
    check(near_rgb(rt, in, 1e-6), "transfer.roundtrip");
  }
  const Rgb neg = space_decode(ColorSpace::kSrgb, Rgb{-0.5f, 0, 0});
  check(neg.r < 0.0f && std::isfinite(neg.r), "transfer.negative");
  const Rgb lin = space_decode(ColorSpace::kLinearSrgb, Rgb{0.3f, 0, 0});
  check(near(lin.r, 0.3, 1e-7), "transfer.linear_identity");

  // Display P3 red primary lies outside the sRGB gamut: in linear sRGB it
  // has r > 1 and slightly negative green/blue.
  const Mat3 p3_to_srgb =
      mat_mul(xyz_to_rgb_matrix(ColorSpace::kSrgb),
              rgb_to_xyz_matrix(ColorSpace::kDisplayP3));
  const Rgb p3_red_in_srgb = mat_apply(p3_to_srgb, Rgb{1, 0, 0});
  check(p3_red_in_srgb.r > 1.1f && p3_red_in_srgb.r < 1.4f &&
            p3_red_in_srgb.g < 0.0f && p3_red_in_srgb.g > -0.1f &&
            p3_red_in_srgb.b < 0.0f && p3_red_in_srgb.b > -0.1f,
        "matrix.p3_to_srgb");
}

void test_interpolation() {
  Lut3D identity = parse_cube_text(identity_cube(2));
  // An identity 2x2x2 LUT reproduces any input exactly (linear function).
  const Rgb sample = lut_sample(identity, Rgb{0.25f, 0.5f, 0.75f});
  check(near_rgb(sample, Rgb{0.25f, 0.5f, 0.75f}, 1e-6), "lut.identity_2");
  // Grid endpoints land exactly on nodes.
  const Rgb corner = lut_sample(identity, Rgb{1.0f, 1.0f, 1.0f});
  check(near_rgb(corner, Rgb{1, 1, 1}, 1e-7), "lut.endpoint");
  // Out-of-domain input clamps to the boundary node values.
  const Rgb clamped = lut_sample(identity, Rgb{-0.5f, 2.0f, 0.5f});
  check(near_rgb(clamped, Rgb{0.0f, 1.0f, 0.5f}, 1e-6), "lut.clamp");

  // Domain mapping: with DOMAIN 0.5..1.0 the value 0.75 sits mid-grid.
  const std::string dom_file = "LUT_3D_SIZE 2\nDOMAIN_MIN 0.5 0.5 0.5\n"
                               "DOMAIN_MAX 1 1 1\n"
                               "0 0 0\n0 0 1\n0 1 0\n0 1 1\n"
                               "1 0 0\n1 0 1\n1 1 0\n1 1 1\n";
  Lut3D dlut = parse_cube_text(dom_file);
  const Rgb mid = lut_sample(dlut, Rgb{0.75f, 0.75f, 0.75f});
  check(near_rgb(mid, Rgb{0.5f, 0.5f, 0.5f}, 1e-6), "lut.domain_mapping");

  // Degenerate size-2 grid with non-identity corners interpolates linearly.
  Lut3D two;
  two.size = 2;
  two.data = {0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 1, 1,
              1, 0, 0, 1, 0, 1, 1, 1, 0, 1, 1, 1};
  const Rgb half = lut_sample(two, Rgb{0.5f, 0.5f, 0.5f});
  check(near_rgb(half, Rgb{0.5f, 0.5f, 0.5f}, 1e-6), "lut.trilinear_center");
}

void test_render_paths() {
  Lut3D lut = parse_cube_text(identity_cube(5));
  std::vector<Rgb> pixels;
  for (int i = 0; i < 1000; ++i) {
    const double t = static_cast<double>(i);
    pixels.push_back(Rgb{static_cast<float>(std::fmod(t * 0.7548, 1.3) - 0.15),
                         static_cast<float>(std::fmod(t * 0.5698, 1.2) - 0.1),
                         static_cast<float>(std::fmod(t * 0.3183, 1.1) - 0.05)});
  }
  RenderOptions opt;
  opt.space = ColorSpace::kDisplayP3;
  opt.clamp_output = false;
  std::vector<Rgb> scalar;
  render_scalar(lut, pixels, scalar, opt);
  for (std::size_t block : {1u, 3u, 257u, 1000u, 4096u}) {
    std::vector<Rgb> tiled;
    render_tiled(lut, pixels, tiled, block, opt);
    check(tiled.size() == scalar.size(), "paths.size");
    bool same = true;
    for (std::size_t i = 0; i < scalar.size(); ++i) {
      same &= scalar[i].r == tiled[i].r && scalar[i].g == tiled[i].g &&
              scalar[i].b == tiled[i].b;
    }
    check(same, "paths.identical");
  }
  bool threw = false;
  try {
    std::vector<Rgb> out;
    render_tiled(lut, pixels, out, 0, opt);
  } catch (const std::invalid_argument&) {
    threw = true;
  }
  check(threw, "paths.block_zero_rejected");
}

void test_histogram() {
  std::vector<Rgb> px = {Rgb{0.0f, 0.5f, 1.0f}, Rgb{-0.1f, 1.5f, 0.999f}};
  const HistogramSet hs = compute_histogram(px, 4);
  check(hs.channel[0].bins[0] == 1 && hs.channel[0].underflow == 1,
        "hist.underflow");
  check(hs.channel[1].bins[2] == 1 && hs.channel[1].overflow == 1,
        "hist.overflow");
  check(hs.channel[2].bins[3] == 2, "hist.endpoint_inclusive");
}

}  // namespace

int run_self_test() {
  test_parser();
  test_color_space();
  test_interpolation();
  test_render_paths();
  test_histogram();
  if (g_failures == 0) {
    std::printf("self_test=passed\n");
    return 0;
  }
  std::fprintf(stderr, "self_test=failed failures=%d\n", g_failures);
  return 1;
}

}  // namespace lutcheck
