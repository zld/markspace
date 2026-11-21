#pragma once
//
// layout_internal.hpp — Helpers shared between layout_engine.cpp (libass
// lifecycle) and layout_event.cpp (per-event rendering). Not part of the
// public interface.
//
#include <cstdint>
#include <string>
#include <vector>

#include <ass/ass.h>

#include "ass_model.hpp"
#include "layout_engine.hpp"

namespace asspipe {

// Compose a single-event ASS script from a fully resolved style.
std::string build_event_script(const AssDocument& doc, const Event& event,
                               const ResolvedStyle& rs, int canvas_w,
                               int canvas_h);

// Composite libass glyph images onto an opaque RGBA canvas.
void blit_image(std::vector<std::uint8_t>& rgba, int canvas_w, int canvas_h,
                const ASS_Image* img);

// True when libass log output looks like a font lookup failure.
bool messages_mention_missing_font(const std::string& messages);

// libass lifecycle state owned by LayoutEngine.
struct LayoutEngine::Impl {
    LayoutConfig cfg;
    ASS_Library* library = nullptr;
    ASS_Renderer* renderer = nullptr;
    std::string messages;

    explicit Impl(LayoutConfig c);
    ~Impl();
};

} // namespace asspipe
