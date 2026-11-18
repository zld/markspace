#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>
#include <cmath>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <string>

#include "color_space.hpp"
#include "interpolation.hpp"
#include "errors.hpp"
#include "interpolation.hpp"
#include "lut_parser.hpp"
#include "ppm.hpp"
#include "color_space.hpp"
#include "interpolation.hpp"

using lutcheck::ExitCode;
using lutcheck::Lut3D;
using lutcheck::Vec3;
using lutcheck::Mat3;
using lutcheck::ColorSpace;

namespace {

Lut3D parse_text(const std::string& text) {
  std::istringstream stream(text);
  return lutcheck::parse_cube(stream, "<memory>");
}

std::string make_cube(int size, const std::string& body_prefix = "") {
  std::ostringstream out;
  out << "TITLE \"generated\"\n";
  out << body_prefix;
  out << "LUT_3D_SIZE " << size << "\n";
  // Identity ramp: value at node (r,g,b) is simply the normalized coordinate.
  for (int b = 0; b < size; ++b) {
    for (int g = 0; g < size; ++g) {
      for (int r = 0; r < size; ++r) {
        const double denom = size - 1;
        out << r / denom << ' ' << g / denom << ' ' << b / denom << '\n';
      }
    }
  }
  return out.str();
}

bool contains(const std::string& haystack, const std::string& needle) {
  return haystack.find(needle) != std::string::npos;
}

template <typename Fn>
std::string error_message(Fn&& fn) {
  try {
    fn();
  } catch (const lutcheck::AppError& error) {
    CHECK(error.code() == ExitCode::Input);
    return error.what();
  }
  FAIL("expected AppError");
  return {};
}

}  // namespace

TEST_CASE("cube parser reads title size domain and samples", "[parser]") {
  std::ostringstream text;
  text << "# leading comment\n\n";
  text << "TITLE \"Skin Grade\"\n";
  text << "LUT_3D_SIZE 2\n";
  text << "DOMAIN_MIN -0.5 -0.5 -0.5\n";
  text << "DOMAIN_MAX 1.5 1.5 1.5\n";
  text << "  0 0 0\n1 0 0\n0 1 0\n1 1 0\n0 0 1\n1 0 1\n0 1 1\n1 1 1\n";
  const Lut3D lut = parse_text(text.str());

  CHECK(lut.title == "Skin Grade");
  CHECK(lut.size == 2);
  CHECK(lut.node_count() == 8);
  CHECK(lut.domain_min.r == Catch::Approx(-0.5));
  CHECK(lut.domain_max.b == Catch::Approx(1.5));
  // Red varies fastest: node (1,0,0) is the second row.
  CHECK(lut.at(1, 0, 0).r == Catch::Approx(1.0));
  CHECK(lut.at(0, 0, 1).b == Catch::Approx(1.0));
}

TEST_CASE("cube parser uses blue-major ordering", "[parser]") {
  const Lut3D lut = parse_text(make_cube(3));
  CHECK(lut.node_count() == 27);
  CHECK(lut.at(2, 1, 0).r == Catch::Approx(1.0));
  CHECK(lut.at(2, 1, 0).g == Catch::Approx(0.5));
  CHECK(lut.at(2, 1, 0).b == Catch::Approx(0.0));
  CHECK(lut.at(0, 2, 2).b == Catch::Approx(1.0));
}

TEST_CASE("cube parser accepts grid bounds", "[parser]") {
  CHECK(parse_text(make_cube(lutcheck::kMinGridSize)).size == 2);
  // A 65 grid is 274625 nodes; generate only the declaration plus rows lazily.
  std::ostringstream text;
  text << "LUT_3D_SIZE 65\n";
  const int size = 65;
  for (int b = 0; b < size; ++b)
    for (int g = 0; g < size; ++g)
      for (int r = 0; r < size; ++r) text << "0 0 0\n";
  CHECK(parse_text(text.str()).node_count() == 274625);
}

TEST_CASE("cube parser rejects malformed input with a line number", "[parser]") {
  SECTION("missing size") {
    const auto message = error_message(
        [] { parse_text("0 0 0\n1 1 1\n"); });
    CHECK(contains(message, "<memory>:1"));
    CHECK(contains(message, "before LUT_3D_SIZE"));
  }
  SECTION("duplicate size") {
    const auto message = error_message([] {
      parse_text("LUT_3D_SIZE 2\nLUT_3D_SIZE 2\n");
    });
    CHECK(contains(message, "<memory>:2"));
    CHECK(contains(message, "duplicate"));
  }
  SECTION("out of range size") {
    const auto message =
        error_message([] { parse_text("LUT_3D_SIZE 66\n"); });
    CHECK(contains(message, "<memory>:1"));
    CHECK(contains(message, "outside supported range"));
  }
  SECTION("non numeric sample") {
    const auto message = error_message([] {
      parse_text("LUT_3D_SIZE 2\n0 0 0\n0 zero 0\n0 0 0\n0 0 0\n");
    });
    CHECK(contains(message, "<memory>:3"));
    CHECK(contains(message, "invalid sample value"));
  }
  SECTION("non finite sample") {
    const auto message = error_message([] {
      parse_text("LUT_3D_SIZE 2\n0 0 0\n0 nan 0\n0 0 0\n0 0 0\n");
    });
    CHECK(contains(message, "<memory>:3"));
  }
  SECTION("row count too low") {
    const auto message = error_message([] {
      parse_text("LUT_3D_SIZE 2\n0 0 0\n0 0 0\n");
    });
    CHECK(contains(message, "expected 8 samples"));
  }
  SECTION("row count too high") {
    const auto message = error_message([] {
      parse_text(
          "LUT_3D_SIZE 2\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n"
          "0 0 0\n0 0 0\n");
    });
    CHECK(contains(message, "<memory>:10"));
    CHECK(contains(message, "too many samples"));
  }
  SECTION("inverted domain") {
    const auto message = error_message([] {
      parse_text(
          "LUT_3D_SIZE 2\nDOMAIN_MIN 1 0 0\nDOMAIN_MAX 0 1 1\n"
          "0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n");
    });
    CHECK(contains(message, "inverted DOMAIN"));
  }
  SECTION("unpaired domain") {
    const auto message = error_message([] {
      parse_text(
          "LUT_3D_SIZE 2\nDOMAIN_MIN 0 0 0\n"
          "0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n0 0 0\n");
    });
    CHECK(contains(message, "DOMAIN_MIN and DOMAIN_MAX"));
  }
}

TEST_CASE("configured grid limit is enforced", "[parser]") {
  lutcheck::ParseLimits limits;
  limits.max_grid_size = 3;
  std::istringstream stream("LUT_3D_SIZE 4\n");
  try {
    lutcheck::parse_cube(stream, "<memory>", limits);
    FAIL("expected rejection");
  } catch (const lutcheck::AppError& error) {
    CHECK(error.code() == ExitCode::Input);
    CHECK(contains(error.what(), "outside supported range"));
  }
}

TEST_CASE("color space matrices round trip", "[color]") {
  for (auto from :
       {ColorSpace::Rgb, ColorSpace::Rec709, ColorSpace::DisplayP3}) {
    for (auto to :
         {ColorSpace::Rgb, ColorSpace::Rec709, ColorSpace::DisplayP3}) {
      const Mat3 forward = lutcheck::build_conversion(from, to);
      const Mat3 backward = lutcheck::build_conversion(to, from);
      const Mat3 identity = backward * forward;
      for (std::size_t i = 0; i < 9; ++i) {
        CHECK(identity.m[i] ==
              Catch::Approx(i % 4 == 0 ? 1.0 : 0.0).margin(1e-12));
      }
    }
  }
}

TEST_CASE("rec709 to p3 maps the white point and primaries", "[color]") {
  const Mat3 to_p3 =
      lutcheck::conversion_matrix(ColorSpace::Rec709, ColorSpace::DisplayP3);
  const Vec3 white = to_p3.apply(Vec3{1.0, 1.0, 1.0});
  CHECK(white.r == Catch::Approx(1.0).margin(1e-12));
  CHECK(white.g == Catch::Approx(1.0).margin(1e-12));
  CHECK(white.b == Catch::Approx(1.0).margin(1e-12));
  // Rec.709 red lies outside the Display-P3 gamut, so it maps to a P3 value
  // with slightly lower red and small positive green/blue.
  const Vec3 red = to_p3.apply(Vec3{1.0, 0.0, 0.0});
  CHECK(red.r == Catch::Approx(0.8224620).margin(1e-6));
  CHECK(red.g == Catch::Approx(0.0331942).margin(1e-6));
  CHECK(red.b == Catch::Approx(0.0170826).margin(1e-6));
  // P3 red, on the other hand, is well outside Rec.709.
  const Mat3 to_709 =
      lutcheck::conversion_matrix(ColorSpace::DisplayP3, ColorSpace::Rec709);
  const Vec3 p3_red = to_709.apply(Vec3{1.0, 0.0, 0.0});
  CHECK(p3_red.r == Catch::Approx(1.2249402).margin(1e-6));
  const Vec3 black = to_p3.apply(Vec3{0.0, 0.0, 0.0});
  CHECK(black.r == Catch::Approx(0.0));
}

TEST_CASE("unknown color space is a usage error", "[color]") {
  ColorSpace space = ColorSpace::Rgb;
  CHECK_FALSE(lutcheck::parse_color_space("adobe-rgb", &space));
  CHECK(lutcheck::parse_color_space("display-p3", &space));
  CHECK(space == ColorSpace::DisplayP3);
  CHECK(lutcheck::parse_color_space("p3", &space));
  CHECK(lutcheck::color_space_name(space) == std::string("display-p3"));
}

TEST_CASE("singular matrix reports a located error", "[color]") {
  Mat3 singular;
  singular.m = {1, 2, 3, 2, 4, 6, 7, 8, 9};
  CHECK(singular.determinant() == Catch::Approx(0.0).margin(1e-12));
  try {
    singular.inverse();
    FAIL("expected singular matrix rejection");
  } catch (const lutcheck::AppError& error) {
    CHECK(error.code() == ExitCode::Input);
    CHECK(contains(error.what(), "singular"));
  }
}

TEST_CASE("trilinear interpolation reproduces grid nodes", "[interp]") {
  const Lut3D lut = parse_text(make_cube(2));
  for (int b = 0; b < 2; ++b) {
    for (int g = 0; g < 2; ++g) {
      for (int r = 0; r < 2; ++r) {
        const Vec3 out =
            lutcheck::sample_trilinear(lut, Vec3{static_cast<double>(r),
                                                 static_cast<double>(g),
                                                 static_cast<double>(b)});
        CHECK(out.r == Catch::Approx(r).margin(1e-12));
        CHECK(out.g == Catch::Approx(g).margin(1e-12));
        CHECK(out.b == Catch::Approx(b).margin(1e-12));
      }
    }
  }
}

TEST_CASE("trilinear interpolation matches an affine LUT exactly", "[interp]") {
  // A LUT whose samples are an affine function is reproduced exactly by the
  // trilinear kernel everywhere, which makes it a strong correctness fixture.
  std::ostringstream text;
  text << "LUT_3D_SIZE 5\n";
  for (int b = 0; b < 5; ++b) {
    for (int g = 0; g < 5; ++g) {
      for (int r = 0; r < 5; ++r) {
        const double rr = r / 4.0, gg = g / 4.0, bb = b / 4.0;
        text << 0.1 + 0.8 * rr << ' ' << 0.2 - 0.1 * gg + 0.5 * bb << ' '
             << 0.3 * rr + 0.6 * bb << '\n';
      }
    }
  }
  const Lut3D lut = parse_text(text.str());
  for (double t : {0.0, 0.05, 0.17, 0.5, 0.9, 1.0}) {
    const Vec3 out = lutcheck::sample_trilinear(lut, Vec3{t, 1.0 - t, 0.25});
    CHECK(out.r == Catch::Approx(0.1 + 0.8 * t).margin(1e-12));
    CHECK(out.g == Catch::Approx(0.2 - 0.1 * (1.0 - t) + 0.5 * 0.25)
                          .margin(1e-12));
    CHECK(out.b == Catch::Approx(0.3 * t + 0.6 * 0.25).margin(1e-12));
  }
}

TEST_CASE("trilinear clamps out of domain input", "[interp]") {
  const Lut3D lut = parse_text(make_cube(4));
  const Vec3 low = lutcheck::sample_trilinear(lut, Vec3{-3.0, -0.001, -7.0});
  CHECK(low.r == Catch::Approx(0.0));
  CHECK(low.g == Catch::Approx(0.0));
  CHECK(low.b == Catch::Approx(0.0));
  const Vec3 high = lutcheck::sample_trilinear(lut, Vec3{9.0, 1.5, 100.0});
  CHECK(high.r == Catch::Approx(1.0));
  CHECK(high.g == Catch::Approx(1.0));
  CHECK(high.b == Catch::Approx(1.0));
}

TEST_CASE("trilinear honours an explicit domain", "[interp]") {
  std::ostringstream text;
  text << "LUT_3D_SIZE 2\nDOMAIN_MIN -1 -1 -1\nDOMAIN_MAX 1 1 1\n";
  for (int b = 0; b < 2; ++b)
    for (int g = 0; g < 2; ++g)
      for (int r = 0; r < 2; ++r)
        text << r << ' ' << g << ' ' << b << '\n';
  const Lut3D lut = parse_text(text.str());
  const Vec3 zero = lutcheck::sample_trilinear(lut, Vec3{0.0, 0.0, 0.0});
  CHECK(zero.r == Catch::Approx(0.5));
  const Vec3 neg = lutcheck::sample_trilinear(lut, Vec3{-1.0, -1.0, -1.0});
  CHECK(neg.r == Catch::Approx(0.0));
  const Vec3 pos = lutcheck::sample_trilinear(lut, Vec3{1.0, 1.0, 1.0});
  CHECK(pos.b == Catch::Approx(1.0));
}

TEST_CASE("ppm round trip preserves pixels", "[ppm]") {
  lutcheck::Image image;
  image.width = 3;
  image.height = 2;
  image.pixels = {{0.0, 0.5, 1.0}, {1.0, 0.0, 0.25}, {0.125, 0.75, 0.5},
                  {0.2, 0.4, 0.6}, {0.9, 0.9, 0.9}, {0.0, 0.0, 0.0}};
  const std::string encoded = lutcheck::encode_ppm(image);
  CHECK(encoded.rfind("P6\n3 2\n255\n", 0) == 0);
  CHECK(encoded.size() == 11 + 6 * 3);
}

TEST_CASE("ppm reader rejects truncated and oversized data", "[ppm]") {
  const std::string dir = "lutcheck_ppm_test";
  std::filesystem::create_directories(dir);
  SECTION("truncated") {
    const std::string path = dir + "/truncated.ppm";
    {
      std::ofstream out(path, std::ios::binary);
      out << "P6\n2 2\n255\n" << "\x01\x02";
    }
    try {
      lutcheck::read_ppm(path);
      FAIL("expected truncation error");
    } catch (const lutcheck::AppError& error) {
      CHECK(error.code() == ExitCode::Input);
      CHECK(contains(error.what(), "truncated pixel data"));
    }
  }
  SECTION("overflowing dimensions") {
    const std::string path = dir + "/huge.ppm";
    {
      std::ofstream out(path, std::ios::binary);
      out << "P6\n65535 65535\n255\n";
    }
    try {
      lutcheck::read_ppm(path);
      FAIL("expected size error");
    } catch (const lutcheck::AppError& error) {
      CHECK(error.code() == ExitCode::Input);
      CHECK(contains(error.what(), "limit is"));
    }
  }
  SECTION("empty file") {
    const std::string path = dir + "/empty.ppm";
    { std::ofstream out(path, std::ios::binary); }
    try {
      lutcheck::read_ppm(path);
      FAIL("expected format error");
    } catch (const lutcheck::AppError& error) {
      CHECK(error.code() == ExitCode::Input);
    }
  }
  std::filesystem::remove_all(dir);
}
