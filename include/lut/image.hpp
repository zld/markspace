#ifndef LUT_IMAGE_HPP
#define LUT_IMAGE_HPP

#include <cstdint>
#include <string>
#include <vector>

#include "lut/model.hpp"

namespace lut {

// In-memory RGB image. Pixels are normalized to [0,1] regardless of the
// source bit depth; the original maxval is retained for round-trip writes.
struct Image {
    std::uint32_t width = 0;
    std::uint32_t height = 0;
    std::uint32_t maxval = 255;
    std::vector<RGB> pixels;

    std::size_t pixelCount() const {
        return static_cast<std::size_t>(width) * height;
    }
};

// Reads a binary PPM (magic P6). Rejects truncated data, dimension overflow,
// illegal maxval and non-P6 variants. Throws IoError on every format problem.
Image readPpm(const std::string& path);

// Serializes a P6 image at the stored maxval (16-bit samples are big-endian
// per the PPM spec). The caller writes through writeFileAtomic.
std::string renderPpm(const Image& image);

// Atomic P6 write combining renderPpm and writeFileAtomic.
void writePpm(const std::string& path, const Image& image);

}  // namespace lut

#endif  // LUT_IMAGE_HPP
