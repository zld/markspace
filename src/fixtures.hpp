#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "types.hpp"

namespace lutcheck {

inline constexpr std::size_t kDefaultFixtureWidth = 128;
inline constexpr std::size_t kDefaultFixtureHeight = 96;

// Deterministic fixtures: identical bytes on every machine and every run.
// No clocks or entropy sources are consulted.
struct FixtureSet {
  std::string lut_path;
  std::string image_path;
  std::string output_path;
};

// Writes `identity`-based but non-trivial grade to `path` as a .cube file.
// The grade blends a smooth S-curve with a small cross-channel mix so that a
// transposed axis or a wrong interpolation would be visible in the sample.
void write_fixture_lut(const std::string& path, int size);

// Builds an in-memory sample image with smooth ramps, skin-like patches,
// specular highlights and deliberately out-of-range values (-0.05 .. 1.15).
// PPM fixtures clamp on encode, but the in-memory image exercises domain
// handling directly.
Image make_fixture_image(std::size_t width, std::size_t height);

// Writes the fixture image as a P6 PPM.
void write_fixture_image(const std::string& path, const Image& image);

// Creates the directory (one level) if it does not exist. Returns false when
// the path exists and is not a directory.
bool ensure_directory(const std::string& path);

// Joins path components with '/'; trivially sufficient for local paths on
// macOS and keeps the CLI free of platform branches.
std::string join_path(const std::string& directory, const std::string& name);

}  // namespace lutcheck
