#include "generators.hpp"

namespace tl {

namespace {

RGB gradient(uint32_t x, uint32_t y, int64_t t, uint32_t w, uint32_t h) {
  uint32_t r = w > 1 ? (x * 255u) / (w - 1) : 0;
  uint32_t g = h > 1 ? (y * 255u) / (h - 1) : 0;
  uint32_t b = static_cast<uint32_t>((static_cast<int64_t>(x) * 3 +
                                      static_cast<int64_t>(y) * 2 + t * 7) &
                                     0xff);
  return {static_cast<uint8_t>(r), static_cast<uint8_t>(g),
          static_cast<uint8_t>(b)};
}

RGB checker(uint32_t x, uint32_t y, int64_t t) {
  // 8px cells drifting diagonally with time
  int64_t cx = (static_cast<int64_t>(x) + t * 3) / 8;
  int64_t cy = (static_cast<int64_t>(y) + t) / 8;
  bool on = ((cx + cy) & 1) != 0;
  return on ? RGB{232, 228, 220} : RGB{28, 30, 44};
}

RGB shapes(uint32_t x, uint32_t y, int64_t t, uint32_t w, uint32_t h) {
  RGB px{16, 18, 26};  // background
  // Circle sweeping horizontally, wrapping around.
  int64_t r = (w < h ? w : h) / 4;
  if (r < 1) r = 1;
  int64_t span = static_cast<int64_t>(w) + 2 * r;
  int64_t cx = (t * 4) % span - r;
  int64_t cy = static_cast<int64_t>(h) / 2;
  int64_t dx = static_cast<int64_t>(x) - cx;
  int64_t dy = static_cast<int64_t>(y) - cy;
  if (dx * dx + dy * dy <= r * r) px = RGB{235, 120, 40};
  // Bar sliding vertically.
  int64_t bh = static_cast<int64_t>(h) / 6;
  if (bh < 1) bh = 1;
  int64_t vspan = static_cast<int64_t>(h) + bh;
  int64_t by = (t * 2) % vspan - bh;
  int64_t bx0 = static_cast<int64_t>(w) / 3;
  int64_t bx1 = 2 * bx0;
  if (static_cast<int64_t>(x) >= bx0 && static_cast<int64_t>(x) < bx1 &&
      static_cast<int64_t>(y) >= by && static_cast<int64_t>(y) < by + bh)
    px = RGB{60, 140, 220};
  return px;
}

}  // namespace

RGB generate_pixel(GenType g, uint32_t x, uint32_t y, int64_t t, uint32_t w,
                   uint32_t h) {
  switch (g) {
    case GenType::Gradient: return gradient(x, y, t, w, h);
    case GenType::Checker: return checker(x, y, t);
    case GenType::Shapes: return shapes(x, y, t, w, h);
  }
  return {};
}

void generate_frame(GenType g, int64_t t, FrameBuffer& out) {
  const uint32_t w = out.width(), h = out.height();
  for (uint32_t y = 0; y < h; ++y)
    for (uint32_t x = 0; x < w; ++x)
      out.at(x, y) = generate_pixel(g, x, y, t, w, h);
}

}  // namespace tl
