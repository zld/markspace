// Deterministic pixel hashing and frame difference statistics.
// Everything is integer-only so results are bit-identical across machines
// and C++ standard library implementations.
#pragma once

#include <cstdint>
#include <string>

#include "frame_buffer.hpp"

namespace tr {

// FNV-1a 64-bit over the exact RGB raster bytes. Fixed-length lowercase hex.
std::uint64_t hashFrame(const FrameBuffer& fb);
std::string toHex64(std::uint64_t value);

struct FrameDiff {
  // Pixels with any channel differing. Range: 0 .. width*height.
  std::size_t diffPixels;
  // Sum of |a-b| over every channel of every pixel.
  std::uint64_t sumAbsDiff;
  // Mean absolute channel difference in 4-decimal fixed point, e.g.
  // formatMeanAbs() == "0.0000" for identical frames. Integer math only.
  double meanAbsDiff() const = delete;  // use formatMeanAbs(); no float path
};

// Throws UsageError when the frame dimensions do not match.
FrameDiff compareFrames(const FrameBuffer& a, const FrameBuffer& b);

// Fixed 4-decimal rendering of (sumAbsDiff / 3 / width / height), range
// "0.0000" .. "255.0000".
std::string formatMeanAbs(const FrameDiff& diff, int width, int height);

}  // namespace tr
