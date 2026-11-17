#pragma once

#include "frame_buffer.hpp"
#include "manifest.hpp"

namespace tl {

// Deterministic procedural sources: pixel value depends only on
// (generator, x, y, t, w, h). Integer math only, so repeated renders of
// the same timeline are bit-identical.
RGB generate_pixel(GenType g, uint32_t x, uint32_t y, int64_t t, uint32_t w,
                   uint32_t h);

void generate_frame(GenType g, int64_t t, FrameBuffer& out);

}  // namespace tl
