#include "layout_engine.hpp"

#include <ass/ass.h>

#include <algorithm>
#include <cstring>

#include "layout_internal.hpp"
#include "style_parser.hpp"
#include "tag_resolver.hpp"

namespace asspipe {

namespace {

// Locate the base style an event should use after its last \r reset.
const Style* base_style(const AssDocument& doc, const Event& event,
                        const EffectiveState& state) {
    const std::string& wanted =
        state.style_name.empty() ? event.style_name : state.style_name;
    auto it = doc.styles.find([&] {
        std::string s = wanted;
        std::transform(s.begin(), s.end(), s.begin(),
                       [](unsigned char c) { return std::tolower(c); });
        return s;
    }());
    return it == doc.styles.end() ? nullptr : &it->second;
}

} // namespace

bool LayoutEngine::render_event(const AssDocument& doc, const Event& event,
                                RenderedEvent& out, std::string& error,
                                std::vector<Diagnostic>& out_diags) {
    Event scratch = event; // tag resolution mutates per-event counters
    EffectiveState state = resolve_event_tags(scratch);
    const Style* st = base_style(doc, event, state);
    if (st == nullptr || !st->valid) {
        // Invalid/cyclic/missing styles never reach libass.
        error = "event " + std::to_string(event.index) +
                " references invalid style '" + event.style_name + "'";
        out_diags.push_back(
            Diagnostic{DiagLevel::Error, DiagKind::RenderFailure, event.line,
                       event.index, error});
        return false;
    }

    ResolvedStyle rs = resolve_style(doc, *st);
    // Inline overrides win over the resolved style, in text order.
    if (state.bold >= 0) rs.bold = state.bold == 0 ? 0 : 1;
    if (state.italic >= 0) rs.italic = state.italic == 0 ? 0 : 1;
    if (state.fontsize > 0) rs.fontsize = state.fontsize;
    if (!state.fontname.empty()) rs.fontname = state.fontname;
    if (state.alignment >= 1 && state.alignment <= 9)
        rs.alignment = state.alignment;

    std::string script = build_event_script(doc, event, rs,
                                            impl_->cfg.canvas.width,
                                            impl_->cfg.canvas.height);
    impl_->messages.clear();
    ASS_Track* track = ass_read_memory(
        impl_->library, script.data(),
        static_cast<size_t>(script.size()), nullptr);
    if (track == nullptr) {
        error = "libass failed to parse composed script for event " +
                std::to_string(event.index);
        out_diags.push_back(
            Diagnostic{DiagLevel::Error, DiagKind::RenderFailure, event.line,
                       event.index, error});
        return false;
    }

    int detect_change = 0;
    ASS_Image* image = ass_render_frame(impl_->renderer, track, 10,
                                        &detect_change);

    // Opaque dark background so glyph coverage is always measurable.
    const int w = impl_->cfg.canvas.width;
    const int h = impl_->cfg.canvas.height;
    out.rgba.assign(static_cast<std::size_t>(w) * h * 4, 0);
    for (std::size_t i = 0; i < out.rgba.size(); i += 4) {
        out.rgba[i + 0] = 24;
        out.rgba[i + 1] = 24;
        out.rgba[i + 2] = 32;
        out.rgba[i + 3] = 255;
    }

    int min_x = w, min_y = h, max_x = -1, max_y = -1;
    for (const ASS_Image* im = image; im; im = im->next) {
        for (int y = 0; y < im->h; ++y) {
            for (int x = 0; x < im->w; ++x) {
                if (im->bitmap[y * im->stride + x] == 0) continue;
                int px = im->dst_x + x;
                int py = im->dst_y + y;
                if (px < 0 || py < 0 || px >= w || py >= h) continue;
                min_x = std::min(min_x, px);
                min_y = std::min(min_y, py);
                max_x = std::max(max_x, px);
                max_y = std::max(max_y, py);
            }
        }
    }
    blit_image(out.rgba, w, h, image);
    ass_free_track(track);

    if (max_x < 0) {
        if (messages_mention_missing_font(impl_->messages)) {
            error = "font unavailable for event " +
                    std::to_string(event.index) + " (family " + rs.fontname +
                    "): " + impl_->messages;
            out_diags.push_back(
                Diagnostic{DiagLevel::Error, DiagKind::FontMissing, event.line,
                           event.index, error});
        } else {
            error = "libass produced no glyphs for event " +
                    std::to_string(event.index);
            out_diags.push_back(
                Diagnostic{DiagLevel::Error, DiagKind::RenderFailure,
                           event.line, event.index, error});
        }
        return false;
    }

    out.event_id = event.index;
    out.x = min_x;
    out.y = min_y;
    out.width = max_x - min_x + 1;
    out.height = max_y - min_y + 1;
    return true;
}

} // namespace asspipe
