#pragma once

#include <map>
#include <string>
#include <vector>

#include "ass_document.hpp"

namespace sp {

// Per-event counts of the inline override tags we understand.
struct TagCounts {
    int bold = 0;
    int italic = 0;
    int underline = 0;
    int strikeout = 0;
    int font_name = 0;
    int font_size = 0;
    int colour = 0;
    int alpha = 0;
    int outline = 0;
    int shadow = 0;
    int alignment = 0;
    int position = 0;
    int move = 0;
    int margin_l = 0;
    int margin_r = 0;
    int margin_v = 0;
    int reset = 0;
    int reset_style = 0;
    int scale_x = 0;
    int scale_y = 0;
    int spacing = 0;
    int rotation = 0;
    int karaoke = 0;
    int clip = 0;
    int drawing = 0;
    int line_break = 0;
    int hard_space = 0;
    int unknown = 0;

    int total() const;
    std::string summary() const;
    std::string to_json() const;
};

// Last override per category, in tag order, as display-ready values.
struct InlineOverride {
    std::map<std::string, std::string> values;  // category -> last value
    std::vector<std::string> trace;             // applied tags in textual order
    std::string last_reset_target;              // \rX argument, empty when \r
    bool has_reset = false;
    bool reset_target_missing = false;
};

struct InlineParse {
    TagCounts counts;
    std::string normalized_text;   // plain text with escapes resolved for display
    InlineOverride overrides;
    std::vector<Diagnostic> diagnostics;
};

InlineParse parse_inline_tags(const std::string& raw_text, int event_id, int line);

// Produces the canonical libass text: the raw text with balanced override
// blocks, unclosed blocks closed at end-of-event, and drawing/override blocks
// left semantically intact.
std::string canonicalize_for_render(const std::string& raw_text);

}  // namespace sp
