#pragma once

#include <cstdint>
#include <string>

#include "frame_buffer.hpp"

namespace tl {

enum class TransitionType { Fade, Wipe, Iris };

constexpr const char* transition_name(TransitionType t) {
  switch (t) {
    case TransitionType::Fade: return "fade";
    case TransitionType::Wipe: return "wipe";
    case TransitionType::Iris: return "iris";
  }
  return "unknown";
}

// Returns false when the name is not one of fade/wipe/iris.
bool parse_transition(const std::string& name, TransitionType& out);

// Blends `from` -> `to` with integer progress num/den in [0,1]
// (num == 0 yields `from`, num == den yields `to`). All pixel math is
// fixed-point integer, so results are bit-identical across machines.
// Throws ArgError on out-of-range progress, DataError on size mismatch.
void apply_transition(TransitionType type, const FrameBuffer& from,
                      const FrameBuffer& to, int64_t progress_num,
                      int64_t progress_den, FrameBuffer& out);

}  // namespace tl
