#ifndef TRANSITION_LAB_TRANSITION_HPP
#define TRANSITION_LAB_TRANSITION_HPP

#include <cstdint>
#include <string>

#include "frame_buffer.hpp"

namespace tl {

// All transition math is 16.16 fixed point: no floating point is used in
// the render path so results are identical across processes/machines.
constexpr int kFpShift = 16;
constexpr std::int64_t kFpScale = 1LL << kFpShift;

enum class TransitionType { Dissolve, Wipe, CircleReveal };

enum class WipeDirection {
    LeftToRight,
    RightToLeft,
    TopToBottom,
    BottomToTop
};

struct TransitionParams {
    WipeDirection direction = WipeDirection::LeftToRight;
    // Soft edge width as a fraction of the wipe extent, numerator/denominator.
    int softness_num = 1;
    int softness_den = 10;
};

const char* transition_type_name(TransitionType type);

// Render one transition frame. progress_num/progress_den describe the
// transition position in [0,1]. Progress 0 yields exactly `from`,
// progress 1 (num == den) yields exactly `to` — the endpoint copies are
// explicit, not inferred from the blend math.
// Returns false (and fills `error`) on invalid arguments.
bool render_transition(FrameBuffer& out, const FrameBuffer& from,
                       const FrameBuffer& to, int progress_num,
                       int progress_den, TransitionType type,
                       const TransitionParams& params, std::string* error);

// Integer square root for a non-negative 64-bit value (floor semantics).
std::uint64_t isqrt64(std::uint64_t value);

}  // namespace tl

#endif
