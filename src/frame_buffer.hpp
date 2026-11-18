// Self-managed RGB frame buffer and binary PPM (P6) IO.
// No third-party graphics code: the only on-disk image format is P6 PPM.
#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

#include "errors.hpp"

namespace tr {

// Hard geometry limits. A single frame never exceeds 4096x4096 = 16 MP,
// i.e. 48 MiB of RGB; the renderer keeps at most two live frames (96 MiB).
constexpr int kMaxDimension = 4096;
constexpr std::size_t kMaxPixels =
    static_cast<std::size_t>(kMaxDimension) * kMaxDimension;
constexpr std::size_t kMaxFrameBytes = 3u * kMaxPixels;  // 50,331,648 bytes

class IoError;  // defined below

class FrameBuffer {
 public:
  FrameBuffer(int width, int height);

  int width() const noexcept { return width_; }
  int height() const noexcept { return height_; }

  std::size_t pixelIndex(int x, int y) const noexcept {
    return (static_cast<std::size_t>(y) * static_cast<std::size_t>(width_) +
            static_cast<std::size_t>(x)) *
           3u;
  }

  void setPixel(int x, int y, std::uint8_t r, std::uint8_t g,
                std::uint8_t b);
  void getPixel(int x, int y, std::uint8_t* r, std::uint8_t* g,
                std::uint8_t* b) const;
  void fill(std::uint8_t r, std::uint8_t g, std::uint8_t b);

  const std::vector<std::uint8_t>& bytes() const noexcept { return pixels_; }
  std::vector<std::uint8_t>& bytesWritable() noexcept { return pixels_; }
  bool sameSize(const FrameBuffer& other) const noexcept {
    return width_ == other.width_ && height_ == other.height_;
  }

 private:
  int width_;
  int height_;
  std::vector<std::uint8_t> pixels_;
};

// Thrown on any PPM/disk failure. Carries the target path so callers can
// report which frame failed; temporary files are removed before throwing.
class IoError : public RenderError {
 public:
  explicit IoError(const std::string& msg) : RenderError(msg) {}
};

// Write P6 PPM to a temp file in the same directory, then atomically rename.
// On failure the temp file is removed and an IoError naming `path` is thrown;
// frames written earlier are left untouched.
void writePpmAtomic(const std::string& path, const FrameBuffer& fb);

// Strict P6 PPM reader (255 maxval, exact pixel byte count). Throws IoError.
FrameBuffer readPpm(const std::string& path);

// Create `dir` (and parents). Throws IoError with the path on failure.
void ensureOutputDir(const std::string& dir);

// Guard against touching unrelated user data: an existing output directory
// may contain only *.ppm files and `manifestName`. Throws UsageError.
void checkOutputDirSafe(const std::string& dir,
                        const std::string& manifestName);

}  // namespace tr
