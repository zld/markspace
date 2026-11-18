#include "color_space.hpp"

#include <cmath>

#include "errors.hpp"

namespace lutcheck {
namespace {

struct Chromaticity {
  double x;
  double y;
};

struct Primaries {
  Chromaticity r;
  Chromaticity g;
  Chromaticity b;
  Chromaticity white;
};

// Both spaces share the D65 white point, so a matrix built from these
// primaries is a pure basis change and does not need chromatic adaptation.
constexpr Primaries kRec709{{0.640, 0.330}, {0.300, 0.600}, {0.150, 0.060},
                            {0.3127, 0.3290}};
constexpr Primaries kDisplayP3{{0.680, 0.320}, {0.265, 0.690}, {0.150, 0.060},
                               {0.3127, 0.3290}};

double det3(const std::array<double, 9>& m) {
  return m[0] * (m[4] * m[8] - m[5] * m[7]) -
         m[1] * (m[3] * m[8] - m[5] * m[6]) +
         m[2] * (m[3] * m[7] - m[4] * m[6]);
}

std::array<double, 9> inverse3(const std::array<double, 9>& m, double det) {
  const double inv = 1.0 / det;
  return {
      (m[4] * m[8] - m[5] * m[7]) * inv,
      (m[2] * m[7] - m[1] * m[8]) * inv,
      (m[1] * m[5] - m[2] * m[4]) * inv,
      (m[5] * m[6] - m[3] * m[8]) * inv,
      (m[0] * m[8] - m[2] * m[6]) * inv,
      (m[2] * m[3] - m[0] * m[5]) * inv,
      (m[3] * m[7] - m[4] * m[6]) * inv,
      (m[1] * m[6] - m[0] * m[7]) * inv,
      (m[0] * m[4] - m[1] * m[3]) * inv,
  };
}

// Standard SMPTE RP 177 style construction: XYZ = M * RGB, with M's columns
// scaled so that RGB (1,1,1) maps exactly to the white point.
Mat3 primaries_to_xyz(const Primaries& p) {
  const auto column = [](const Chromaticity& c) {
    return std::array<double, 3>{c.x / c.y, 1.0, (1.0 - c.x - c.y) / c.y};
  };
  const auto r = column(p.r);
  const auto g = column(p.g);
  const auto b = column(p.b);
  // Columns are RGB -> XYZ; Z of each primary is 1 - x - y.
  const std::array<double, 9> unscaled{
      r[0], g[0], b[0],
      r[1], g[1], b[1],
      r[2], g[2], b[2],
  };
  const double det = det3(unscaled);
  if (std::abs(det) < 1e-12) {
    fail(ExitCode::Input, "color space primaries produce a singular matrix");
  }
  const auto inv = inverse3(unscaled, det);
  const double wx = p.white.x / p.white.y;
  const double wy = 1.0;
  const double wz = (1.0 - p.white.x - p.white.y) / p.white.y;
  const double scale_r = inv[0] * wx + inv[1] * wy + inv[2] * wz;
  const double scale_g = inv[3] * wx + inv[4] * wy + inv[5] * wz;
  const double scale_b = inv[6] * wx + inv[7] * wy + inv[8] * wz;
  return {{{unscaled[0] * scale_r, unscaled[1] * scale_g, unscaled[2] * scale_b,
            unscaled[3] * scale_r, unscaled[4] * scale_g, unscaled[5] * scale_b,
            unscaled[6] * scale_r, unscaled[7] * scale_g,
            unscaled[8] * scale_b}}};
}

Mat3 multiply(const Mat3& a, const Mat3& b) {
  Mat3 out;
  for (std::size_t row = 0; row < 3; ++row) {
    for (std::size_t col = 0; col < 3; ++col) {
      double sum = 0.0;
      for (std::size_t k = 0; k < 3; ++k) {
        sum += a.m[row * 3 + k] * b.m[k * 3 + col];
      }
      out.m[row * 3 + col] = sum;
    }
  }
  return out;
}

// `Rgb` is the untagged working space: it shares Rec.709 primaries, which is
// also the sRGB basis. Rec.709 and Display-P3 are defined by the primaries
// above, all against D65 so no chromatic adaptation is needed.
Mat3 to_xyz(ColorSpace space) {
  switch (space) {
    case ColorSpace::Rgb:
    case ColorSpace::Rec709:
      return primaries_to_xyz(kRec709);
    case ColorSpace::DisplayP3:
      return primaries_to_xyz(kDisplayP3);
  }
  fail(ExitCode::Usage, "unknown color space");
}

}  // namespace

Mat3 operator*(const Mat3& a, const Mat3& b) { return multiply(a, b); }

const char* color_space_name(ColorSpace space) {
  switch (space) {
    case ColorSpace::Rgb:
      return "rgb";
    case ColorSpace::Rec709:
      return "rec709";
    case ColorSpace::DisplayP3:
      return "display-p3";
  }
  return "unknown";
}

bool parse_color_space(const std::string& text, ColorSpace* out) {
  ColorSpace value;
  if (text == "rgb") {
    value = ColorSpace::Rgb;
  } else if (text == "rec709") {
    value = ColorSpace::Rec709;
  } else if (text == "display-p3" || text == "p3") {
    value = ColorSpace::DisplayP3;
  } else {
    return false;
  }
  if (out != nullptr) *out = value;
  return true;
}

std::string known_color_spaces() { return "rgb, rec709, display-p3"; }

Mat3 Mat3::identity() { return {{{1.0, 0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0, 1.0}}}; }

Vec3 Mat3::apply(const Vec3& v) const {
  return {m[0] * v.r + m[1] * v.g + m[2] * v.b,
          m[3] * v.r + m[4] * v.g + m[5] * v.b,
          m[6] * v.r + m[7] * v.g + m[8] * v.b};
}

double Mat3::determinant() const { return det3(m); }

Mat3 Mat3::inverse() const {
  const double det = determinant();
  if (std::abs(det) < 1e-12) {
    fail(ExitCode::Input, "matrix is singular and cannot be inverted");
  }
  return Mat3{inverse3(m, det)};
}

Mat3 conversion_matrix(ColorSpace from, ColorSpace to) {
  if (from == to) return Mat3::identity();
  const Mat3 from_xyz = to_xyz(from);
  const Mat3 to_xyz_matrix = to_xyz(to);
  // to_xyz() is a primaries matrix, not necessarily orthonormal; the caller
  // needs the inverse for the reverse direction.
  return to_xyz_matrix.inverse() * from_xyz;
}

Mat3 build_conversion(ColorSpace from, ColorSpace to) {
  Mat3 matrix = conversion_matrix(from, to);
  if (!std::isfinite(matrix.determinant()) ||
      std::abs(matrix.determinant()) < 1e-12) {
    fail(ExitCode::Input, std::string("conversion ") + color_space_name(from) +
                              " -> " + color_space_name(to) +
                              " is not invertible");
  }
  return matrix;
}

}  // namespace lutcheck
