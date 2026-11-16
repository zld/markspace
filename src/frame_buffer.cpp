#include "frame_buffer.hpp"

#include <algorithm>

namespace tl {

FrameBuffer::FrameBuffer(int width, int height)
    : width_(width), height_(height) {
    if (valid()) {
        pixels_.resize(static_cast<std::size_t>(width_) *
                       static_cast<std::size_t>(height_));
    }
}

bool FrameBuffer::dimensions_match(const FrameBuffer& other) const {
    return width_ == other.width_ && height_ == other.height_;
}

bool valid_dimensions(int width, int height) {
    if (width <= 0 || height <= 0) return false;
    if (width > kMaxWidth || height > kMaxHeight) return false;
    const long long pixels = static_cast<long long>(width) * height;
    if (pixels > kMaxPixels) return false;
    return true;
}

std::uint64_t splitmix64(std::uint64_t& state) {
    std::uint64_t z = (state += 0x9e3779b97f4a7c15ULL);
    z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9ULL;
    z = (z ^ (z >> 27)) * 0x94d049bb133111ebULL;
    return z ^ (z >> 31);
}

namespace {

std::uint8_t clamp_byte(int value) {
    return static_cast<std::uint8_t>(std::clamp(value, 0, 255));
}

}  // namespace

FrameBuffer make_checkerboard(int width, int height, int frame_index,
                              int cell_size, std::uint64_t seed) {
    FrameBuffer fb;
    if (!valid_dimensions(width, height) || cell_size <= 0) return fb;
    fb = FrameBuffer(width, height);

    std::uint64_t state = seed ^
        (static_cast<std::uint64_t>(frame_index) * 0xd1b54a32d192ed03ULL);
    const Pixel color_a{
        clamp_byte(static_cast<int>(splitmix64(state) & 0xff)),
        clamp_byte(static_cast<int>((splitmix64(state) >> 16) & 0xff)),
        clamp_byte(static_cast<int>((splitmix64(state) >> 32) & 0xff))};
    const Pixel color_b{
        clamp_byte(static_cast<int>((splitmix64(state) >> 8) & 0xff)),
        clamp_byte(static_cast<int>(splitmix64(state) & 0xff)),
        clamp_byte(static_cast<int>((splitmix64(state) >> 24) & 0xff))};

    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            const int cell_x = x / cell_size;
            const int cell_y = y / cell_size;
            const bool parity = ((cell_x + cell_y) & 1) == 0;
            fb.at(x, y) = parity ? color_a : color_b;
        }
    }
    return fb;
}

FrameBuffer make_gradient(int width, int height, int frame_index,
                          std::uint64_t seed) {
    FrameBuffer fb;
    if (!valid_dimensions(width, height)) return fb;
    fb = FrameBuffer(width, height);

    const int shift = static_cast<int>((seed + frame_index * 7) & 0xff);
    for (int y = 0; y < height; ++y) {
        for (int x = 0; x < width; ++x) {
            const int r = (x * 255) / std::max(1, width - 1);
            const int g = (y * 255) / std::max(1, height - 1);
            const int b = (r + g + shift) & 0xff;
            fb.at(x, y) = Pixel{clamp_byte(r), clamp_byte(g), clamp_byte(b)};
        }
    }
    return fb;
}

}  // namespace tl
