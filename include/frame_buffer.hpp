#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace tl {

// Hard resource limits (constraint: bound frame size and total frame count).
inline constexpr int kMaxDimension = 2048;              // max width/height
inline constexpr long long kMaxPixelsPerFrame = 1LL << 22;
inline constexpr long long kMaxFramesPerSequence = 100000;
inline constexpr long long kMaxTotalBytes = 512LL << 20;  // buffered sequence cap

// Self-contained RGB8 frame buffer.
class FrameBuffer {
 public:
  FrameBuffer() = default;
  FrameBuffer(int width, int height);  // throws ArgError on invalid size

  int width() const { return width_; }
  int height() const { return height_; }
  const std::vector<uint8_t>& pixels() const { return pixels_; }
  std::vector<uint8_t>& pixels() { return pixels_; }

  // RGB8 access, no bounds check (hot path).
  const uint8_t* pixel(int x, int y) const {
    return &pixels_[(static_cast<size_t>(y) * width_ + x) * 3];
  }
  uint8_t* pixel(int x, int y) {
    return &pixels_[(static_cast<size_t>(y) * width_ + x) * 3];
  }

 private:
  int width_ = 0;
  int height_ = 0;
  std::vector<uint8_t> pixels_;
};

void validate_dimensions(int width, int height);  // throws ArgError

// FNV-1a 64-bit over the raw RGB payload.
uint64_t fnv1a64(const uint8_t* data, size_t size);
uint64_t hash_frame(const FrameBuffer& fb);

// Per-frame comparison against the previous frame.
struct FrameDiff {
  long long changed_pixels = 0;         // pixels where any channel differs
  long long mean_abs_delta_milli = 0;   // mean |channel delta| * 1000
};
FrameDiff diff_frames(const FrameBuffer& a, const FrameBuffer& b);

}  // namespace tl
