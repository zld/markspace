#ifndef TRANSITION_LAB_FRAME_BUFFER_HPP
#define TRANSITION_LAB_FRAME_BUFFER_HPP

#include <cstdint>
#include <vector>

namespace tl {

// Hard safety limits used by every frame-producing code path.
constexpr int kMaxWidth = 4096;
constexpr int kMaxHeight = 4096;
constexpr int kMaxPixels = 8'000'000;
constexpr int kMaxFrames = 4096;
constexpr std::size_t kMaxPathLen = 1024;

struct Pixel {
    std::uint8_t r = 0;
    std::uint8_t g = 0;
    std::uint8_t b = 0;
};

// Owns a contiguous RGB8 frame. Width/height are immutable after
// construction so the row indexing convention stays unambiguous.
class FrameBuffer {
public:
    FrameBuffer() = default;
    FrameBuffer(int width, int height);

    int width() const { return width_; }
    int height() const { return height_; }
    bool valid() const { return width_ > 0 && height_ > 0; }

    Pixel& at(int x, int y) { return pixels_[static_cast<std::size_t>(y) * width_ + x]; }
    const Pixel& at(int x, int y) const {
        return pixels_[static_cast<std::size_t>(y) * width_ + x];
    }

    std::uint8_t* raw() { return reinterpret_cast<std::uint8_t*>(pixels_.data()); }
    const std::uint8_t* raw() const {
        return reinterpret_cast<const std::uint8_t*>(pixels_.data());
    }
    std::size_t byte_size() const { return pixels_.size() * sizeof(Pixel); }

    bool dimensions_match(const FrameBuffer& other) const;

private:
    int width_ = 0;
    int height_ = 0;
    std::vector<Pixel> pixels_;
};

bool valid_dimensions(int width, int height);

// Deterministic 64-bit PRNG (SplitMix64). Same seed always yields the
// same stream, which is what makes cross-process replay reproducible.
std::uint64_t splitmix64(std::uint64_t& state);

// Generate one checkerboard frame. The two cell colors are derived from
// the frame index and seed so successive frames differ predictably.
FrameBuffer make_checkerboard(int width, int height, int frame_index,
                              int cell_size, std::uint64_t seed);

// Generate one horizontal/vertical/diagonal-ish gradient frame. The blue
// band shifts with the frame index and seed.
FrameBuffer make_gradient(int width, int height, int frame_index,
                          std::uint64_t seed);

}  // namespace tl

#endif
