#pragma once
//
// layout_engine.hpp — Convert parsed events into libass input and obtain
// the rendered region (x, y, width, height) of each subtitle.
//
// The engine reuses the parser's ResolvedStyle data so the style fed to
// libass is exactly the one derived from inheritance resolution.
//
#include <cstdint>
#include <string>
#include <vector>

#include "ass_model.hpp"
#include "tag_resolver.hpp"

namespace asspipe {

struct Canvas {
    int width = 1280;
    int height = 720;
};

// One event's rendered result: tight bounding box in canvas coordinates
// plus an RGBA8 buffer of the whole canvas (stride == width*4).
struct RenderedEvent {
    int event_id = 0;
    int x = 0;
    int y = 0;
    int width = 0;
    int height = 0;
    std::vector<std::uint8_t> rgba; // width*height*4, opaque background
};

struct LayoutConfig {
    Canvas canvas;
    std::string fonts_dir;  // optional extra font directory, "" = system only
};

// Parse "WxH" (e.g. "1280x720"). Returns false on malformed input.
bool parse_canvas(const std::string& spec, Canvas& out);

class LayoutEngine {
public:
    explicit LayoutEngine(LayoutConfig cfg);
    ~LayoutEngine();

    LayoutEngine(const LayoutEngine&) = delete;
    LayoutEngine& operator=(const LayoutEngine&) = delete;

    // Render one event. Returns false and fills `error` (and records a
    // FontMissing/RenderFailure diagnostic in `out_diags`) on failure.
    bool render_event(const AssDocument& doc, const Event& event,
                      RenderedEvent& out, std::string& error,
                      std::vector<Diagnostic>& out_diags);

private:
    struct Impl;
    Impl* impl_;
};

} // namespace asspipe
