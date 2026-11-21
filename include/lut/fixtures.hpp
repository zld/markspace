#ifndef LUT_FIXTURES_HPP
#define LUT_FIXTURES_HPP

#include <string>

#include "lut/image.hpp"
#include "lut/model.hpp"

namespace lut {

inline constexpr int kFixtureLutSize = 25;
inline constexpr std::uint32_t kFixtureImageSide = 64;

// A smooth, deliberately non-identity LUT: gentle per-channel lifts plus
// cross-channel mixing. Every node stays inside [0,1] so histogram totals
// cover all pixels without out-of-domain noise.
Lut3D makeFixtureLut(int size = kFixtureLutSize);

// A deterministic 64x64 RGB patch field (4096 pixels) with varied hues. The
// underlying linear ramps are encoded through `space`'s transfer function,
// so different spaces produce distinct, space-legal encoded samples.
Image makeFixtureImage(ColorSpaceId space = ColorSpaceId::Srgb,
                       std::uint32_t maxval = 255);

// Writes source.cube and patches.ppm under `outDir` (created if needed).
void writeFixtures(const std::string& outDir, ColorSpaceId patchSpace);

}  // namespace lut

#endif  // LUT_FIXTURES_HPP
