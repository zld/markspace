#include "interpolation.hpp"

#include <algorithm>
#include <cmath>

namespace lutcheck {
namespace {

double clamp01(double value) { return std::clamp(value, 0.0, 1.0); }

// Position along one axis after applying the LUT domain. A degenerate grid of
// two nodes still yields a valid [0, 1] interval because the parser rejects
// inverted domains.
struct AxisPosition {
  std::size_t lower = 0;
  std::size_t upper = 0;
  double fraction = 0.0;
};

AxisPosition locate(double normalized, int size) {
  const double scaled = clamp01(normalized) * static_cast<double>(size - 1);
  const double floor_value = std::floor(scaled);
  std::size_t lower = static_cast<std::size_t>(floor_value);
  if (lower >= static_cast<std::size_t>(size - 1)) {
    lower = static_cast<std::size_t>(size - 2);
  }
  const std::size_t upper = lower + 1;
  const double fraction =
      std::clamp(scaled - static_cast<double>(lower), 0.0, 1.0);
  return {lower, upper, fraction};
}

Vec3 lerp(const Vec3& a, const Vec3& b, double t) {
  return {a.r + (b.r - a.r) * t, a.g + (b.g - a.g) * t,
          a.b + (b.b - a.b) * t};
}

}  // namespace

Vec3 normalize_domain(const Vec3& input, const Vec3& domain_min,
                      const Vec3& domain_max) {
  Vec3 out;
  for (std::size_t i = 0; i < 3; ++i) {
    const double span = domain_max[i] - domain_min[i];
    // The parser guarantees a positive span; guard anyway so a hand-built LUT
    // cannot divide by zero.
    out[i] = span > 0.0 ? clamp01((input[i] - domain_min[i]) / span) : 0.0;
  }
  return out;
}

Vec3 sample_trilinear(const Lut3D& lut, const Vec3& input) {
  if (lut.size < kMinGridSize) {
    return {};
  }
  const Vec3 normalized =
      normalize_domain(input, lut.domain_min, lut.domain_max);
  // .cube axes are ordered red-fastest, then green, then blue.
  const AxisPosition r = locate(normalized.r, lut.size);
  const AxisPosition g = locate(normalized.g, lut.size);
  const AxisPosition b = locate(normalized.b, lut.size);

  const Vec3 c000 = lut.at(static_cast<int>(r.lower), static_cast<int>(g.lower),
                           static_cast<int>(b.lower));
  const Vec3 c100 = lut.at(static_cast<int>(r.upper), static_cast<int>(g.lower),
                           static_cast<int>(b.lower));
  const Vec3 c010 = lut.at(static_cast<int>(r.lower), static_cast<int>(g.upper),
                           static_cast<int>(b.lower));
  const Vec3 c110 = lut.at(static_cast<int>(r.upper), static_cast<int>(g.upper),
                           static_cast<int>(b.lower));
  const Vec3 c001 = lut.at(static_cast<int>(r.lower), static_cast<int>(g.lower),
                           static_cast<int>(b.upper));
  const Vec3 c101 = lut.at(static_cast<int>(r.upper), static_cast<int>(g.lower),
                           static_cast<int>(b.upper));
  const Vec3 c011 = lut.at(static_cast<int>(r.lower), static_cast<int>(g.upper),
                           static_cast<int>(b.upper));
  const Vec3 c111 = lut.at(static_cast<int>(r.upper), static_cast<int>(g.upper),
                           static_cast<int>(b.upper));

  const Vec3 c00 = lerp(c000, c100, r.fraction);
  const Vec3 c10 = lerp(c010, c110, r.fraction);
  const Vec3 c01 = lerp(c001, c101, r.fraction);
  const Vec3 c11 = lerp(c011, c111, r.fraction);
  const Vec3 c0 = lerp(c00, c10, g.fraction);
  const Vec3 c1 = lerp(c01, c11, g.fraction);
  return lerp(c0, c1, b.fraction);
}

}  // namespace lutcheck
