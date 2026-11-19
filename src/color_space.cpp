// color_space.cpp - RGB/XYZ matrices and transfer functions for
// Linear sRGB, sRGB and Display P3. All conversions happen in linear light.
#include <cmath>

#include "color_types.hpp"

namespace lutcheck {

Mat3 mat_mul(const Mat3& a, const Mat3& b) {
  Mat3 out;
  for (int r = 0; r < 3; ++r) {
    for (int c = 0; c < 3; ++c) {
      double sum = 0.0;
      for (int k = 0; k < 3; ++k) sum += a.m[r][k] * b.m[k][c];
      out.m[r][c] = sum;
    }
  }
  return out;
}

Mat3 mat_inverse(const Mat3& m) {
  const double(&a)[3][3] = m.m;
  const double cof00 = a[1][1] * a[2][2] - a[1][2] * a[2][1];
  const double cof01 = a[0][2] * a[2][1] - a[0][1] * a[2][2];
  const double cof02 = a[0][1] * a[1][2] - a[0][2] * a[1][1];
  const double det = a[0][0] * cof00 + a[1][0] * cof01 + a[2][0] * cof02;
  Mat3 out;
  const double inv = 1.0 / det;
  out.m[0][0] = cof00 * inv;
  out.m[0][1] = cof01 * inv;
  out.m[0][2] = cof02 * inv;
  out.m[1][0] = (a[1][2] * a[2][0] - a[1][0] * a[2][2]) * inv;
  out.m[1][1] = (a[0][0] * a[2][2] - a[0][2] * a[2][0]) * inv;
  out.m[1][2] = (a[0][2] * a[1][0] - a[0][0] * a[1][2]) * inv;
  out.m[2][0] = (a[1][0] * a[2][1] - a[1][1] * a[2][0]) * inv;
  out.m[2][1] = (a[0][1] * a[2][0] - a[0][0] * a[2][1]) * inv;
  out.m[2][2] = (a[0][0] * a[1][1] - a[0][1] * a[1][0]) * inv;
  return out;
}

Rgb mat_apply(const Mat3& m, const Rgb& v) {
  Rgb out;
  out.r = static_cast<float>(m.m[0][0] * v.r + m.m[0][1] * v.g + m.m[0][2] * v.b);
  out.g = static_cast<float>(m.m[1][0] * v.r + m.m[1][1] * v.g + m.m[1][2] * v.b);
  out.b = static_cast<float>(m.m[2][0] * v.r + m.m[2][1] * v.g + m.m[2][2] * v.b);
  return out;
}

bool parse_color_space(const std::string& text, ColorSpace& out) {
  if (text == "linear-srgb") {
    out = ColorSpace::kLinearSrgb;
    return true;
  }
  if (text == "srgb") {
    out = ColorSpace::kSrgb;
    return true;
  }
  if (text == "display-p3") {
    out = ColorSpace::kDisplayP3;
    return true;
  }
  return false;
}

const char* color_space_name(ColorSpace space) {
  switch (space) {
    case ColorSpace::kLinearSrgb:
      return "linear-srgb";
    case ColorSpace::kSrgb:
      return "srgb";
    case ColorSpace::kDisplayP3:
      return "display-p3";
  }
  return "unknown";
}

Mat3 rgb_to_xyz_matrix(ColorSpace space) {
  // D65-adapted primaries. Linear sRGB and sRGB share primaries.
  if (space == ColorSpace::kDisplayP3) {
    return Mat3{{{0.486570948648216, 0.265667693169093, 0.198217285234362},
                 {0.228974564069749, 0.691738521836506, 0.079286914093745},
                 {0.000000000000000, 0.045113381858903, 1.043944368900976}}};
  }
  return Mat3{{{0.412390799265959, 0.357584339383878, 0.180480788401834},
               {0.212639005871510, 0.715168678767756, 0.072192315360734},
               {0.019330818715592, 0.119194779794626, 0.950532152249661}}};
}

Mat3 xyz_to_rgb_matrix(ColorSpace space) {
  return mat_inverse(rgb_to_xyz_matrix(space));
}

namespace {

// sRGB electro-optical curve, mirrored for negative inputs so out-of-range
// values round-trip instead of producing NaN.
double srgb_decode_channel(double c) {
  const double a = std::fabs(c);
  const double v =
      a <= 0.04045 ? a / 12.92 : std::pow((a + 0.055) / 1.055, 2.4);
  return std::copysign(v, c);
}

double srgb_encode_channel(double l) {
  const double a = std::fabs(l);
  const double v =
      a <= 0.0031308 ? 12.92 * a : 1.055 * std::pow(a, 1.0 / 2.4) - 0.055;
  return std::copysign(v, l);
}

Rgb apply_per_channel(const Rgb& v, double (*fn)(double)) {
  Rgb out;
  out.r = static_cast<float>(fn(v.r));
  out.g = static_cast<float>(fn(v.g));
  out.b = static_cast<float>(fn(v.b));
  return out;
}

Rgb identity(const Rgb& v) { return v; }

}  // namespace

Rgb space_decode(ColorSpace space, const Rgb& v) {
  if (space == ColorSpace::kLinearSrgb) return identity(v);
  // Display P3 uses the sRGB transfer function.
  return apply_per_channel(v, srgb_decode_channel);
}

Rgb space_encode(ColorSpace space, const Rgb& v) {
  if (space == ColorSpace::kLinearSrgb) return identity(v);
  return apply_per_channel(v, srgb_encode_channel);
}

}  // namespace lutcheck
