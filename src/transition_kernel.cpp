#include "transition_kernel.hpp"

namespace tl {

namespace {

uint8_t lerp_channel(uint8_t a, uint8_t b, Rational p) {
  // round-half-up of a*(1-p) + b*p with exact integers
  int64_t v = static_cast<int64_t>(a) * (p.den - p.num) +
              static_cast<int64_t>(b) * p.num;
  return static_cast<uint8_t>((v + p.den / 2) / p.den);
}

RGB crossfade(RGB a, RGB b, Rational p) {
  return {lerp_channel(a.r, b.r, p), lerp_channel(a.g, b.g, p),
          lerp_channel(a.b, b.b, p)};
}

// Left-to-right wipe. B where x/w < p, tie (x/w == p) stays A.
RGB wipe(RGB a, RGB b, uint32_t x, uint32_t w, Rational p) {
  bool take_b = static_cast<__int128>(x) * p.den <
                static_cast<__int128>(p.num) * w;
  return take_b ? b : a;
}

// Circle expanding from the frame center. B where dist < p * max_dist,
// tie stays A. Coordinates are doubled so the center is exact for both
// even and odd sizes.
RGB radial(RGB a, RGB b, uint32_t x, uint32_t y, uint32_t w, uint32_t h,
           Rational p) {
  int64_t dx = 2 * static_cast<int64_t>(x) - (static_cast<int64_t>(w) - 1);
  int64_t dy = 2 * static_cast<int64_t>(y) - (static_cast<int64_t>(h) - 1);
  __int128 dist2 = static_cast<__int128>(dx) * dx + static_cast<__int128>(dy) * dy;
  int64_t mx = static_cast<int64_t>(w) - 1;
  int64_t my = static_cast<int64_t>(h) - 1;
  __int128 max2 = static_cast<__int128>(mx) * mx + static_cast<__int128>(my) * my;
  __int128 lhs = dist2 * p.den * p.den;
  __int128 rhs = static_cast<__int128>(p.num) * p.num * max2;
  return lhs < rhs ? b : a;
}

}  // namespace

RGB kernel_blend(KernelType k, RGB a, RGB b, uint32_t x, uint32_t y,
                 uint32_t w, uint32_t h, Rational p) {
  // Endpoints are exact for every kernel.
  if (p.num <= 0) return a;
  if (p.num >= p.den) return b;
  switch (k) {
    case KernelType::Crossfade: return crossfade(a, b, p);
    case KernelType::Wipe: return wipe(a, b, x, w, p);
    case KernelType::Radial: return radial(a, b, x, y, w, h, p);
  }
  return a;
}

}  // namespace tl
