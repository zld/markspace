#pragma once

#include "lut_parser.hpp"
#include "types.hpp"

namespace lutcheck {

// Maps a sample from its declared input domain into [0, 1]. Values outside
// the domain are clamped; this keeps out-of-range negatives and highlights
// stable instead of extrapolating into nonsense.
Vec3 normalize_domain(const Vec3& input, const Vec3& domain_min,
                      const Vec3& domain_max);

// The single trilinear kernel shared by both render paths. `lut` must be a
// parsed 3D LUT. Red varies fastest in the grid, so neighbouring samples on
// the red axis are contiguous in memory.
Vec3 sample_trilinear(const Lut3D& lut, const Vec3& input);

}  // namespace lutcheck
