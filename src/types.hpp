#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <vector>

namespace lutcheck {

// A linear-light floating point RGB sample. Values outside [0, 1] are valid
// during processing; interpolation clamps only when mapping into the LUT
// domain.
struct Vec3 {
  double r = 0.0;
  double g = 0.0;
  double b = 0.0;

  double& operator[](std::size_t i) { return i == 0 ? r : (i == 1 ? g : b); }
  double operator[](std::size_t i) const { return i == 0 ? r : (i == 1 ? g : b); }
};

inline Vec3 operator+(const Vec3& a, const Vec3& b) {
  return {a.r + b.r, a.g + b.g, a.b + b.b};
}

inline Vec3 operator-(const Vec3& a, const Vec3& b) {
  return {a.r - b.r, a.g - b.g, a.b - b.b};
}

inline Vec3 operator*(const Vec3& a, double s) {
  return {a.r * s, a.g * s, a.b * s};
}

struct Image {
  std::uint32_t width = 0;
  std::uint32_t height = 0;
  std::vector<Vec3> pixels;

  std::size_t pixel_count() const { return pixels.size(); }
};

}  // namespace lutcheck
