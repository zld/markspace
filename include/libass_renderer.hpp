// subtitle-audit: offscreen libass rendering.
#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "layout.hpp"

namespace subaudit {

// Bounding box of non-transparent pixels on the canvas plus a stable
// 16-bit digest of the composited frame. A fully transparent frame
// yields x=y=w=h=0 and pixel_digest=0.
struct RenderedRegion {
    int event_id = 0;
    int x = 0, y = 0, w = 0, h = 0;
    std::uint16_t pixel_digest = 0;
};

// Renders every layout input at its midpoint on a canvas_w x canvas_h
// frame. jobs must be within 1..8; each worker thread owns an independent
// libass library/renderer context, and results come back in event order
// regardless of the thread count. Returns false with the failing stage
// in err on render errors.
bool render_regions(const std::vector<LayoutInput>& inputs, int canvas_w, int canvas_h,
                    int jobs, std::vector<RenderedRegion>& out, std::string& err);

}  // namespace subaudit
