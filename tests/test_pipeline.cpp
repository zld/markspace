#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>
#include <cmath>
#include <sstream>
#include <string>

#include "color_space.hpp"
#include "errors.hpp"
#include "interpolation.hpp"
#include "lut_parser.hpp"

using lutcheck::ExitCode;
using lutcheck::Lut3D;
using lutcheck::Vec3;

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
