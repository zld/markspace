#include "biome.hpp"

namespace tl {

namespace {

uint32_t lattice_hash(int x, int y, uint32_t seed) {
  uint32_t h = seed + static_cast<uint32_t>(x) * 0x9E3779B1u +
               static_cast<uint32_t>(y) * 0x85EBCA77u;
  h ^= h >> 15;
  h *= 0x2C1B3C6Du;
  h ^= h >> 12;
  h *= 0x297A2D39u;
  h ^= h >> 15;
  return h;
}

// Value noise at 8.8 fixed-point coordinates, returns 0..255.
int noise256(int fx, int fy, uint32_t seed) {
  const int x0 = fx >> 8, y0 = fy >> 8;
  const int tx = fx & 255, ty = fy & 255;
  // Smoothstep 3t^2 - 2t^3 in 8.8 fixed point.
  const int sx = (tx * tx * (768 - 2 * tx)) >> 16;
  const int sy = (ty * ty * (768 - 2 * ty)) >> 16;
  const int v00 = lattice_hash(x0, y0, seed) & 255;
  const int v10 = lattice_hash(x0 + 1, y0, seed) & 255;
  const int v01 = lattice_hash(x0, y0 + 1, seed) & 255;
  const int v11 = lattice_hash(x0 + 1, y0 + 1, seed) & 255;
  const int top = v00 + ((v10 - v00) * sx >> 8);
  const int bot = v01 + ((v11 - v01) * sx >> 8);
  return top + ((bot - top) * sy >> 8);
}

// Fractal heightfield, 4 octaves, returns 0..255.
int height_at(int x, int y, int w, int h, uint32_t seed) {
  int sum = 0, amp = 128, total = 0;
  for (int o = 0; o < 4; ++o) {
    const int cells = 3 << o;  // lattice cells across the frame
    const int fx = static_cast<int>(static_cast<int64_t>(x) * cells * 256 / w);
    const int fy = static_cast<int>(static_cast<int64_t>(y) * cells * 256 / h);
    sum += noise256(fx, fy, seed + static_cast<uint32_t>(o) * 101u) * amp;
    total += 255 * amp;
    amp >>= 1;
  }
  return sum * 255 / total;
}

}  // namespace

FrameBuffer make_biome_frame(uint32_t seed, int width, int height,
                             int variant) {
  FrameBuffer fb(width, height);
  const int snow_line = variant == 0 ? 210 : 150;  // winter lowers it
  for (int y = 0; y < height; ++y) {
    for (int x = 0; x < width; ++x) {
      const int hv = height_at(x, y, width, height, seed);
      uint8_t r, g, b;
      if (hv < 70) {        // water
        r = 30; g = 60; b = 140;
      } else if (hv < 90) {  // sand
        r = 194; g = 178; b = 128;
      } else if (hv < 170) {  // grass
        r = 60; g = 130; b = 60;
      } else if (hv < snow_line) {  // rock
        r = 110; g = 100; b = 95;
      } else {  // snow
        r = 235; g = 240; b = 245;
      }
      if (variant == 1) {  // night: darken, cool shift
        r = static_cast<uint8_t>(r * 40 >> 8);
        g = static_cast<uint8_t>(g * 48 >> 8);
        b = static_cast<uint8_t>((b * 64 >> 8) + 10);
      }
      uint8_t* p = fb.pixel(x, y);
      p[0] = r;
      p[1] = g;
      p[2] = b;
    }
  }
  return fb;
}

}  // namespace tl
