#pragma once

#include <cstddef>
#include <string>

#include "types.hpp"

namespace lutcheck {

inline constexpr std::size_t kMaxPixels = 10'000'000;

// Reads a binary or ASCII PPM (P6/P3). Values are normalized to [0, 1].
// Truncated pixel data, missing headers and oversized images are reported
// with ExitCode::Input.
Image read_ppm(const std::string& path);

// Writes a binary P6 PPM, clamping to [0, 1]. The output is written to a
// temporary sibling and renamed only on success, so a cancelled or failed
// batch never leaves a partially written file behind.
void write_ppm_atomic(const std::string& path, const Image& image,
                      std::size_t max_pixels = kMaxPixels);

// Encodes an image in memory; used by tests and by the atomic writer.
std::string encode_ppm(const Image& image);

}  // namespace lutcheck
