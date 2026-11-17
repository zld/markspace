#pragma once

#include "frame_buffer.hpp"
#include "manifest.hpp"

namespace tl {

// Blends frame B over frame A at progress p in [0, 1] for pixel (x, y).
// Contract: p == 0 yields exactly A, p == 1 yields exactly B; threshold
// ties resolve to A. All math is exact integer/rational — no floats.
RGB kernel_blend(KernelType k, RGB a, RGB b, uint32_t x, uint32_t y,
                 uint32_t w, uint32_t h, Rational p);

}  // namespace tl
