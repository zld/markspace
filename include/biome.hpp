#pragma once

#include <cstdint>

#include "frame_buffer.hpp"

namespace tl {

// Procedurally generated biome keyframe, no external assets. The pixels
// are fully determined by (seed, width, height, variant):
//   variant 0 = day / summer, variant 1 = night / winter.
FrameBuffer make_biome_frame(uint32_t seed, int width, int height, int variant);

}  // namespace tl
