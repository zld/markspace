#pragma once

#include <cstdint>
#include <stdexcept>
#include <vector>

namespace tl {

// Longest allowed frame side.
constexpr uint32_t kMaxSide = 4096;

struct RGB {
  uint8_t r = 0, g = 0, b = 0;
};

static_assert(sizeof(RGB) == 3, "RGB must be tightly packed for P6 output");

inline bool operator==(const RGB& a, const RGB& b) {
  return a.r == b.r && a.g == b.g && a.b == b.b;
}
inline bool operator!=(const RGB& a, const RGB& b) { return !(a == b); }

// Bounded RGB frame. Dimensions are validated at construction: each side
// must be in [1, kMaxSide] and the pixel count is overflow-checked.
class FrameBuffer {
 public:
  FrameBuffer(uint32_t width, uint32_t height) : w_(width), h_(height) {
    if (width == 0 || height == 0 || width > kMaxSide || height > kMaxSide) {
      throw std::invalid_argument("frame dimensions out of range");
    }
    // With both sides <= kMaxSide the product fits easily, but keep the
    // overflow check explicit so the invariant is local to this class.
    if (static_cast<uint64_t>(width) * height > UINT32_MAX) {
      throw std::invalid_argument("frame pixel count overflow");
    }
    px_.resize(static_cast<size_t>(width) * height);
  }

  uint32_t width() const { return w_; }
  uint32_t height() const { return h_; }
  size_t size() const { return px_.size(); }

  RGB& at(uint32_t x, uint32_t y) {
    return px_[static_cast<size_t>(y) * w_ + x];
  }
  const RGB& at(uint32_t x, uint32_t y) const {
    return px_[static_cast<size_t>(y) * w_ + x];
  }

  RGB* data() { return px_.data(); }
  const RGB* data() const { return px_.data(); }

 private:
  uint32_t w_, h_;
  std::vector<RGB> px_;
};

}  // namespace tl
