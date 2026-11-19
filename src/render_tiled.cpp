// render_tiled.cpp - blocked/batched grading path. Reuses the exact same
// per-pixel kernel as the scalar path so results must match bit-for-bit.
#include <algorithm>
#include <stdexcept>

#include "color_types.hpp"

namespace lutcheck {

void render_tiled(const Lut3D& lut, const std::vector<Rgb>& input,
                  std::vector<Rgb>& output, std::size_t block_size,
                  const RenderOptions& opt) {
  if (block_size == 0) {
    throw std::invalid_argument("render_tiled: block_size must be >= 1");
  }
  output.resize(input.size());
  for (std::size_t base = 0; base < input.size(); base += block_size) {
    const std::size_t end = std::min(base + block_size, input.size());
    for (std::size_t i = base; i < end; ++i) {
      output[i] = grade_pixel(lut, input[i], opt);
    }
  }
}

}  // namespace lutcheck
