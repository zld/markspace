#pragma once
//
// tag_resolver.hpp — Inline override-block ("{...}") resolution.
//
// Tags apply in text order; \r restores a named (or the base) style.
// Unclosed blocks, empty blocks and adjacent blocks are handled without
// dropping any visible text.
//
#include <string>

#include "ass_model.hpp"

namespace asspipe {

// Effective style state at the end of an event's text, after all override
// blocks have been applied in order.
struct EffectiveState {
    std::string style_name;  // style selected by the last \r, base initially
    int bold = -1;           // -1 = not overridden -> use style value
    int italic = -1;
    int fontsize = 0;        // 0 = not overridden
    std::string fontname;    // empty = not overridden
    std::string colour;      // last \c value, empty if none
    int alpha = -1;          // 0..255, -1 if none
    int alignment = 0;       // last \an, 0 if none
    bool has_pos = false;
    int pos_x = 0;
    int pos_y = 0;
};

// Scan `text`, populate counts and unclosed_block, and return the final
// effective state. Visible text (anything outside braces) is returned via
// `visible_text` so callers can prove no text was swallowed.
EffectiveState resolve_tags(const std::string& text, TagCounts& counts,
                            bool& unclosed_block,
                            std::string& visible_text);

// Convenience overload writing results directly onto an event.
EffectiveState resolve_event_tags(Event& event);

} // namespace asspipe
