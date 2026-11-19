#pragma once

#include <map>
#include <string>
#include <vector>

#include "ass_document.hpp"

namespace sp {

struct Adjustment {
    std::string field;
    std::string original;
    std::string fallback;
    std::string source;
};

struct EffStyle {
    struct Text {
        std::string value;
        std::string source;
        bool cleared = false;
    };
    struct Number {
        double value = 0;
        std::string source;
        bool cleared = false;
    };
    struct Integer {
        int value = 0;
        std::string source;
        bool cleared = false;
    };

    Text font_name;
    Number font_size;
    Integer bold;
    Integer italic;
    Integer underline;
    Integer strikeout;
    Integer primary_colour;    // 0xAABBGGRR, AA is opacity (0 opaque)
    Integer secondary_colour;
    Integer outline_colour;
    Integer back_colour;
    Number border_style;
    Number outline;
    Number shadow;
    Integer alignment;
    Integer margin_l;
    Integer margin_r;
    Integer margin_v;
    Number scale_x;
    Number scale_y;
    Number spacing;
    Number angle;
    Integer encoding;

    std::vector<Adjustment> adjustments;
    std::vector<std::string> cleared_fields;

    bool font_cleared() const { return font_name.cleared || font_name.value.empty(); }
};

struct ChainLink {
    std::string name;
    int line = 0;
    bool missing = false;
};

struct StyleResolution {
    std::string requested;
    std::vector<ChainLink> chain;  // root -> requested (or partial on cycles)
    EffStyle style;
    std::vector<Diagnostic> diagnostics;
};

EffStyle builtin_style();
bool parse_ass_colour(const std::string& raw, int& out);
std::string colour_to_ass_string(int value);

// Resolves one style, following Parent links, merging fields root -> leaf and
// recording per-field provenance. Cycle and missing-parent cases terminate
// with a locatable diagnostic instead of recursing.
StyleResolution resolve_style(const Document& doc, const std::string& name);

// Resolves every defined style once; returns diagnostics for the whole graph.
std::vector<Diagnostic> resolve_all_styles(const Document& doc,
                                           std::vector<StyleResolution>& out);

// Applies non-zero event margin fields on top of a resolved style.
void apply_event_margins(EffStyle& style, const Event& event, int canvas_w, int canvas_h);

const char* field_source(const std::string& source);

}  // namespace sp
