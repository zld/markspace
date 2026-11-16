#include "verify.hpp"

#include <cstdlib>

namespace tl {

namespace {
constexpr std::uint64_t kFnvOffset = 0xcbf29ce484222325ULL;
constexpr std::uint64_t kFnvPrime = 0x100000001b3ULL;
}  // namespace

std::uint64_t hash_frame(const FrameBuffer& frame) {
    std::uint64_t hash = kFnvOffset;
    const std::uint8_t* data = frame.raw();
    const std::size_t bytes = frame.byte_size();
    for (std::size_t i = 0; i < bytes; ++i) {
        hash ^= static_cast<std::uint64_t>(data[i]);
        hash *= kFnvPrime;
    }
    return hash;
}

FrameStats compute_stats(const FrameBuffer& frame,
                         const FrameBuffer& reference) {
    FrameStats stats;
    if (!frame.valid() || !reference.valid() ||
        !frame.dimensions_match(reference)) {
        return stats;
    }
    const int pixels = frame.width() * frame.height();
    std::uint64_t total_abs = 0;
    for (int y = 0; y < frame.height(); ++y) {
        for (int x = 0; x < frame.width(); ++x) {
            const Pixel& a = frame.at(x, y);
            const Pixel& b = reference.at(x, y);
            const bool differs = (a.r != b.r || a.g != b.g || a.b != b.b);
            if (differs) ++stats.changed_pixels;
            total_abs += static_cast<std::uint64_t>(
                std::abs(static_cast<int>(a.r) - b.r) +
                std::abs(static_cast<int>(a.g) - b.g) +
                std::abs(static_cast<int>(a.b) - b.b));
        }
    }
    // Mean over channels, hence the factor of 3.
    stats.mae = static_cast<double>(total_abs) /
                static_cast<double>(pixels * 3);
    return stats;
}

}  // namespace tl
