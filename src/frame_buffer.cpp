#include "frame_buffer.hpp"

#include <cstdlib>
#include <string>

#include "errors.hpp"

namespace tl {

void validate_dimensions(int width, int height) {
  if (width <= 0 || height <= 0) {
    throw ArgError("frame dimensions must be positive, got " +
                   std::to_string(width) + "x" + std::to_string(height));
  }
  if (width > kMaxDimension || height > kMaxDimension) {
    throw ArgError("frame dimension " + std::to_string(width) + "x" +
                   std::to_string(height) + " exceeds limit " +
                   std::to_string(kMaxDimension));
  }
  if (static_cast<long long>(width) * height > kMaxPixelsPerFrame) {
    throw ArgError("frame " + std::to_string(width) + "x" +
                   std::to_string(height) + " exceeds pixel limit " +
                   std::to_string(kMaxPixelsPerFrame));
  }
}

FrameBuffer::FrameBuffer(int width, int height) : width_(width), height_(height) {
  validate_dimensions(width, height);
  pixels_.assign(static_cast<size_t>(width) * height * 3, 0);
}

uint64_t fnv1a64(const uint8_t* data, size_t size) {
  uint64_t h = 14695981039346656037ull;
  for (size_t i = 0; i < size; ++i) {
    h ^= data[i];
    h *= 1099511628211ull;
  }
  return h;
}

uint64_t hash_frame(const FrameBuffer& fb) {
  return fnv1a64(fb.pixels().data(), fb.pixels().size());
}

FrameDiff diff_frames(const FrameBuffer& a, const FrameBuffer& b) {
  if (a.width() != b.width() || a.height() != b.height()) {
    throw DataError("diff_frames: size mismatch " + std::to_string(a.width()) +
                    "x" + std::to_string(a.height()) + " vs " +
                    std::to_string(b.width()) + "x" + std::to_string(b.height()));
  }
  FrameDiff d;
  const std::vector<uint8_t>& pa = a.pixels();
  const std::vector<uint8_t>& pb = b.pixels();
  const size_t n = pa.size();
  long long abs_sum = 0;
  for (size_t i = 0; i < n; i += 3) {
    int dr = std::abs(static_cast<int>(pa[i]) - pb[i]);
    int dg = std::abs(static_cast<int>(pa[i + 1]) - pb[i + 1]);
    int db = std::abs(static_cast<int>(pa[i + 2]) - pb[i + 2]);
    if (dr | dg | db) ++d.changed_pixels;
    abs_sum += dr + dg + db;
  }
  // Mean over all channels, scaled by 1000, rounded to nearest.
  d.mean_abs_delta_milli =
      (abs_sum * 1000 + static_cast<long long>(n) / 2) / static_cast<long long>(n);
  return d;
}

}  // namespace tl
