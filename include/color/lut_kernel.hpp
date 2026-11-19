#pragma once

#include "color/types.hpp"

namespace color {

// Maps a value in the LUT domain to grid index coordinates. The caller must
// have already converted the value into the LUT's working space.
RGB domainToGrid(const LUT& lut, const RGB& value);

// Trilinear lookup on raw (continuous) grid coordinates. Coordinates are
// clamped to the valid cell range, so a coordinate exactly on the maximum
// domain value (or a hair above it from rounding) samples the last grid node
// instead of reading out of bounds. This is the single interpolation kernel
// shared by every render path.
RGB sampleLut(const LUT& lut, const RGB& grid);

// Converts, bounds-checks and interpolates one pixel. Throws ProcessingError
// with the pixel index when the policy is Reject and any channel lies outside
// the LUT domain, and ColorError for non-finite coordinates.
RGB applyLut(const LUT& lut, const RGB& value, BoundaryPolicy policy,
             std::size_t pixelIndex = kNoPixel);

}  // namespace color
