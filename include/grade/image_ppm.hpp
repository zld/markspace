#ifndef GRADE_IMAGE_PPM_HPP
#define GRADE_IMAGE_PPM_HPP

#include <array>
#include <cstdint>
#include <string>
#include <vector>

namespace grade {

constexpr int kMaxImageDimension = 32767;
constexpr int kPpmChannels = 3;
constexpr int kMaxPpmMaxval = 65535;

// P6 PPM image with samples normalized to doubles in [0, 1].
// Pixel layout is row-major, RGB triplets.
class PpmImage {
public:
    using Pixel = std::array<double, kPpmChannels>;

    PpmImage() = default;
    PpmImage(int width, int height);

    int width() const noexcept { return width_; }
    int height() const noexcept { return height_; }
    size_t pixelCount() const noexcept { return pixels_.size(); }
    int maxval() const noexcept { return maxval_; }

    const std::vector<Pixel>& pixels() const noexcept { return pixels_; }
    std::vector<Pixel>& pixels() noexcept { return pixels_; }

    const Pixel& at(size_t index) const { return pixels_[index]; }
    Pixel& at(size_t index) { return pixels_[index]; }

private:
    friend PpmImage parsePpmBytes(const std::vector<uint8_t>& bytes);
    friend void writePpmFileAtomic(const std::string& path,
                                   const PpmImage& image);

    int width_ = 0;
    int height_ = 0;
    int maxval_ = 255;
    std::vector<Pixel> pixels_;
};

// Reads a binary P6 PPM (8- or 16-bit). Rejects non-P6 magic, comments in
// illegal positions, dimension overflow, illegal maxval and truncated data;
// throws FormatError for content problems and IoError for read failures.
PpmImage readPpmFile(const std::string& path);

// Writes 16-bit big-endian P6 PPM via a same-directory temporary file,
// fsync, then atomic rename; a failed render can never leave a complete
// output at the target path. Throws IoError on any filesystem failure.
void writePpmFileAtomic(const std::string& path, const PpmImage& image);

// String-backed entry point used by tests.
PpmImage parsePpmBytes(const std::vector<uint8_t>& bytes);

} // namespace grade

#endif
