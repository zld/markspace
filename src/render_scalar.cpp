// render_scalar.cpp - straightforward per-pixel grading path.
#include "color_types.hpp"

namespace lutcheck {

void render_scalar(const Lut3D& lut, const std::vector<Rgb>& input,
                   std::vector<Rgb>& output, const RenderOptions& opt) {
  output.resize(input.size());
  for (std::size_t i = 0; i < input.size(); ++i) {
    output[i] = grade_pixel(lut, input[i], opt);
  }
}

}  // namespace lutcheck
