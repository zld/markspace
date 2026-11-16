#ifndef TRANSITION_LAB_VERIFY_HPP
#define TRANSITION_LAB_VERIFY_HPP

#include <cstdint>
#include <vector>

#include "frame_buffer.hpp"

namespace tl {

// FNV-1a 64-bit hash over the tightly packed RGB bytes of a frame.
// Purely arithmetic, so it is reproducible across processes and machines.
std::uint64_t hash_frame(const FrameBuffer& frame);

struct FrameStats {
    long long changed_pixels = 0;
    // Mean absolute per-channel difference, accumulated in fixed point
    // and exposed as a value in [0,255] (two decimals).
    double mae = 0.0;
};

// Compare a frame against the front clip frame: count pixels that differ
// in any channel and compute the mean absolute channel difference.
FrameStats compute_stats(const FrameBuffer& frame,
                         const FrameBuffer& reference);

struct ReplayReport {
    std::size_t frames = 0;
    std::size_t mismatch_frames = 0;
    int first_mismatch = -1;
    std::uint64_t first_hash = 0;
    std::uint64_t last_hash = 0;
    std::vector<std::uint64_t> hashes;
};

}  // namespace tl

#endif
