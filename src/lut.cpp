// lut.cpp - LUT grid sampling (domain mapping + trilinear interpolation)
// and the shared per-pixel grading kernel.
#include <algorithm>
#include <cmath>

#include "color_types.hpp"

namespace lutcheck {

Rgb lut_sample(const Lut3D& lut, const Rgb& in) {
  const int n = lut.size;
  const float vin[3] = {in.r, in.g, in.b};

  // Map domain -> [0, n-1] grid coordinates, clamping out-of-domain input.
  double coord[3];
  for (int c = 0; c < 3; ++c) {
    const double span = lut.domain_max[c] - lut.domain_min[c];
    double t = (static_cast<double>(vin[c]) - lut.domain_min[c]) / span;
    t *= static_cast<double>(n - 1);
    if (!(t > 0.0)) {
      t = 0.0;  // also catches NaN
    } else if (t > static_cast<double>(n - 1)) {
      t = static_cast<double>(n - 1);
    }
    coord[c] = t;
  }

  // Cell indices and fractions. i0 is capped at n-2 so i1 = i0+1 stays in
  // range; at the top endpoint the fraction becomes exactly 1. Works for
  // the degenerate n = 2 case (single cell).
  int i0[3], i1[3];
  double f[3];
  for (int c = 0; c < 3; ++c) {
    int base = static_cast<int>(std::floor(coord[c]));
    if (base > n - 2) base = n - 2;
    i0[c] = base;
    i1[c] = base + 1;
    f[c] = coord[c] - static_cast<double>(base);
  }

  double out[3] = {0.0, 0.0, 0.0};
  for (int dr = 0; dr <= 1; ++dr) {
    const double wr = dr ? f[0] : 1.0 - f[0];
    const int ri = dr ? i1[0] : i0[0];
    for (int dg = 0; dg <= 1; ++dg) {
      const double wg = dg ? f[1] : 1.0 - f[1];
      const int gi = dg ? i1[1] : i0[1];
      for (int db = 0; db <= 1; ++db) {
        const double wb = db ? f[2] : 1.0 - f[2];
        const int bi = db ? i1[2] : i0[2];
        const double w = wr * wg * wb;
        const std::size_t idx = lut.node_index(ri, gi, bi);
        for (int c = 0; c < 3; ++c) {
          out[c] += w * static_cast<double>(lut.data[idx + c]);
        }
      }
    }
  }

  Rgb result;
  result.r = static_cast<float>(out[0]);
  result.g = static_cast<float>(out[1]);
  result.b = static_cast<float>(out[2]);
  return result;
}

Rgb grade_pixel(const Lut3D& lut, const Rgb& in, const RenderOptions& opt) {
  const Rgb linear = space_decode(opt.space, in);
  const Rgb graded = lut_sample(lut, linear);
  Rgb encoded = space_encode(opt.space, graded);
  if (opt.clamp_output) {
    encoded.r = std::min(1.0f, std::max(0.0f, encoded.r));
    encoded.g = std::min(1.0f, std::max(0.0f, encoded.g));
    encoded.b = std::min(1.0f, std::max(0.0f, encoded.b));
  }
  return encoded;
}

}  // namespace lutcheck
