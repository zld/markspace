#include "style_resolver.hpp"

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <set>

namespace sp {

namespace {

constexpr double kFontSizeMin = 1.0;
constexpr double kFontSizeMax = 1024.0;
constexpr double kOutlineMax = 100.0;
constexpr double kShadowMax = 100.0;
constexpr double kScaleMin = 1.0;
constexpr double kScaleMax = 1000.0;
constexpr int kMarginMax = 100000;

std::string fmt_double(double v) {
    char buf[64];
    std::snprintf(buf, sizeof buf, "%g", v);
    return buf;
}

std::string fmt_int(int v) {
    return std::to_string(v);
}

// Parses one raw field into text/number/integer, applying the safety clamp for
// the numeric kinds. `explicit_empty` clears the inherited value.
struct ParseOutcome {
    bool ok = false;
    bool cleared = false;
    std::string text;
    double number = 0;
    int integer = 0;
};

bool parse_number_value(const std::string& raw, double& out, std::string& why) {
    std::string text = trim_ascii(raw);
    if (text.empty()) { why = "empty numeric field"; return false; }
    try {
        size_t used = 0;
        double v = std::stod(text, &used);
        if (used != text.size() || std::isnan(v) || std::isinf(v)) {
            why = "not a finite number: '" + sanitize_for_output(text, 40) + "'";
            return false;
        }
        out = v;
        return true;
    } catch (...) {
        why = "not a number: '" + sanitize_for_output(text, 40) + "'";
        return false;
    }
}

void adjust(EffStyle& style, const std::string& field, const std::string& original,
            const std::string& fallback, const std::string& source) {
    style.adjustments.push_back(Adjustment{field, original, fallback, source});
}

bool parse_colour_value(const std::string& raw, int& out, std::string& why) {
    if (!parse_ass_colour(raw, out)) {
        why = "not an ASS colour: '" + sanitize_for_output(raw, 40) + "'";
        return false;
    }
    return true;
}

bool clamp_number(double& value, double lo, double hi, double& clamped) {
    if (value < lo) { clamped = lo; return true; }
    if (value > hi) { clamped = hi; return true; }
    clamped = value;
    return false;
}

}  // namespace

EffStyle builtin_style() {
    EffStyle s;
    auto set = [](auto& slot, auto value, const char* source) {
        slot.value = value;
        slot.source = source;
    };
    set(s.font_name, std::string("Arial"), "builtin");
    set(s.font_size, 48.0, "builtin");
    set(s.bold, 0, "builtin");
    set(s.italic, 0, "builtin");
    set(s.underline, 0, "builtin");
    set(s.strikeout, 0, "builtin");
    set(s.primary_colour, static_cast<int>(0x00FFFFFF), "builtin");
    set(s.secondary_colour, static_cast<int>(0x00FFFFFF), "builtin");
    set(s.outline_colour, static_cast<int>(0x00000000), "builtin");
    set(s.back_colour, static_cast<int>(0x00000000), "builtin");
    set(s.border_style, 1.0, "builtin");
    set(s.outline, 0.0, "builtin");
    set(s.shadow, 0.0, "builtin");
    set(s.alignment, 2, "builtin");
    set(s.margin_l, 20, "builtin");
    set(s.margin_r, 20, "builtin");
    set(s.margin_v, 20, "builtin");
    set(s.scale_x, 100.0, "builtin");
    set(s.scale_y, 100.0, "builtin");
    set(s.spacing, 0.0, "builtin");
    set(s.angle, 0.0, "builtin");
    set(s.encoding, 1, "builtin");
    return s;
}

bool parse_ass_colour(const std::string& raw, int& out) {
    std::string text = trim_ascii(raw);
    if (text.empty()) return false;
    if (text.size() > 2 && (text.rfind("&H", 0) == 0 || text.rfind("&h", 0) == 0)) {
        std::string hex = text.substr(2);
        if (!hex.empty() && (hex.back() == '&')) hex.pop_back();
        if (hex.empty() || hex.size() > 8) return false;
        for (char ch : hex) {
            if (!std::isxdigit(static_cast<unsigned char>(ch))) return false;
        }
        unsigned long long value = 0;
        try {
            value = std::stoull(hex, nullptr, 16);
        } catch (...) {
            return false;
        }
        // ASS colours are AABBGGRR; short hex forms are BBGGRR with alpha 00.
        out = static_cast<int>(value & 0xFFFFFFFFull);
        return true;
    }
    // Decimal fallback: plain 32-bit integer.
    try {
        size_t used = 0;
        long long v = std::stoll(text, &used, 10);
        if (used != text.size() || v < 0 || v > 0xFFFFFFFFll) return false;
        out = static_cast<int>(v);
        return true;
    } catch (...) {
        return false;
    }
}

std::string colour_to_ass_string(int value) {
    char buf[16];
    std::snprintf(buf, sizeof buf, "&H%08X", static_cast<unsigned>(value));
    return buf;
}

const char* field_source(const std::string& source) {
    return source.empty() ? "builtin" : source.c_str();
}

namespace {

void apply_text_field(EffStyle& s, EffStyle::Text& slot, const RawStyle& raw, const std::string& field,
                      const std::string& display, const char* fallback_why);
void apply_number_field(EffStyle& s, EffStyle::Number& slot, const RawStyle& raw, const std::string& field,
                        double lo, double hi, double fallback);
void apply_integer_field(EffStyle& s, EffStyle::Integer& slot, const RawStyle& raw, const std::string& field,
                         int lo, int hi, int fallback);


void apply_text_field(EffStyle& s, EffStyle::Text& slot, const RawStyle& raw, const std::string& field,
                      const std::string& /*display*/, const char* /*fallback_why*/) {
    auto it = raw.fields.find(field);
    if (it == raw.fields.end() || !it->second.present) return;
    if (it->second.explicit_empty) {
        slot.value.clear();
        slot.cleared = true;
        slot.source = "style: " + raw.name;
        s.cleared_fields.push_back(field);
        return;
    }
    slot.value = it->second.value;
    slot.cleared = false;
    slot.source = "style: " + raw.name;
}

void apply_number_field(EffStyle& s, EffStyle::Number& slot, const RawStyle& raw, const std::string& field,
                        double lo, double hi, double fallback) {
    auto it = raw.fields.find(field);
    if (it == raw.fields.end() || !it->second.present) return;
    if (it->second.explicit_empty) {
        slot.value = fallback;
        slot.cleared = true;
        slot.source = "style: " + raw.name;
        s.cleared_fields.push_back(field);
        return;
    }
    double parsed = 0;
    std::string why;
    if (!parse_number_value(it->second.value, parsed, why)) {
        slot.value = fallback;
        slot.cleared = false;
        slot.source = "style: " + raw.name;
        adjust(s, field, it->second.value, fmt_double(fallback) + " (" + why + ")",
               "style: " + raw.name);
        return;
    }
    double clamped = 0;
    bool out_of_range = clamp_number(parsed, lo, hi, clamped);
    if (out_of_range) {
        adjust(s, field, it->second.value, fmt_double(clamped) + " (clamped to [" + fmt_double(lo) +
                                                          ", " + fmt_double(hi) + "])",
               "style: " + raw.name);
    }
    slot.value = clamped;
    slot.cleared = false;
    slot.source = "style: " + raw.name;
}

void apply_integer_field(EffStyle& s, EffStyle::Integer& slot, const RawStyle& raw, const std::string& field,
                         int lo, int hi, int fallback) {
    auto it = raw.fields.find(field);
    if (it == raw.fields.end() || !it->second.present) return;
    if (it->second.explicit_empty) {
        slot.value = fallback;
        slot.cleared = true;
        slot.source = "style: " + raw.name;
        s.cleared_fields.push_back(field);
        return;
    }
    std::string text = trim_ascii(it->second.value);
    try {
        size_t used = 0;
        long v = std::stol(text, &used);
        if (used != text.size()) throw std::invalid_argument("trailing");
        int iv = static_cast<int>(v);
        if (v < lo || v > hi) {
            int clamped = std::max(lo, std::min(hi, iv));
            adjust(s, field, it->second.value, fmt_int(clamped) + " (clamped to [" + fmt_int(lo) + ", " +
                                                              fmt_int(hi) + "])",
                   "style: " + raw.name);
            iv = clamped;
        }
        slot.value = iv;
        slot.cleared = false;
        slot.source = "style: " + raw.name;
    } catch (...) {
        slot.value = fallback;
        slot.cleared = false;
        slot.source = "style: " + raw.name;
        adjust(s, field, it->second.value, fmt_int(fallback) + " (not an integer)", "style: " + raw.name);
    }
}

// Colour fields carry a 32-bit value that is not range-clamped; invalid input
// falls back to the inherited value with an adjustment record.
void apply_colour_field(EffStyle& s, EffStyle::Integer& slot, const RawStyle& raw, const std::string& field,
                        int fallback) {
    auto it = raw.fields.find(field);
    if (it == raw.fields.end() || !it->second.present) return;
    if (it->second.explicit_empty) {
        slot.value = fallback;
        slot.cleared = true;
        slot.source = "style: " + raw.name;
        s.cleared_fields.push_back(field);
        return;
    }
    int parsed = 0;
    std::string why;
    if (!parse_colour_value(it->second.value, parsed, why)) {
        slot.value = fallback;
        slot.cleared = false;
        slot.source = "style: " + raw.name;
        adjust(s, field, it->second.value, colour_to_ass_string(fallback) + " (" + why + ")",
               "style: " + raw.name);
        return;
    }
    slot.value = parsed;
    slot.cleared = false;
    slot.source = "style: " + raw.name;
}

void merge_style(EffStyle& s, const RawStyle& raw) {
    apply_text_field(s, s.font_name, raw, "fontname", "Fontname", "empty font name");
    apply_number_field(s, s.font_size, raw, "fontsize", kFontSizeMin, kFontSizeMax, 48.0);
    apply_integer_field(s, s.bold, raw, "bold", 0, 1, 0);
    apply_integer_field(s, s.italic, raw, "italic", 0, 1, 0);
    apply_integer_field(s, s.underline, raw, "underline", 0, 1, 0);
    apply_integer_field(s, s.strikeout, raw, "strikeout", 0, 1, 0);
    apply_colour_field(s, s.primary_colour, raw, "primarycolour", 0x00FFFFFF);
    apply_colour_field(s, s.secondary_colour, raw, "secondarycolour", 0x00FFFFFF);
    apply_colour_field(s, s.outline_colour, raw, "outlinecolour", 0x00000000);
    apply_colour_field(s, s.back_colour, raw, "backcolour", 0x00000000);
    apply_number_field(s, s.border_style, raw, "borderstyle", 1.0, 3.0, 1.0);
    apply_number_field(s, s.outline, raw, "outline", 0.0, kOutlineMax, 0.0);
    apply_number_field(s, s.shadow, raw, "shadow", 0.0, kShadowMax, 0.0);
    apply_integer_field(s, s.alignment, raw, "alignment", 1, 9, 2);
    apply_integer_field(s, s.margin_l, raw, "marginl", 0, kMarginMax, 20);
    apply_integer_field(s, s.margin_r, raw, "marginr", 0, kMarginMax, 20);
    apply_integer_field(s, s.margin_v, raw, "marginv", 0, kMarginMax, 20);
    apply_number_field(s, s.scale_x, raw, "scalex", kScaleMin, kScaleMax, 100.0);
    apply_number_field(s, s.scale_y, raw, "scaley", kScaleMin, kScaleMax, 100.0);
    apply_number_field(s, s.spacing, raw, "spacing", -100.0, 100.0, 0.0);
    apply_number_field(s, s.angle, raw, "angle", -360.0, 360.0, 0.0);
    apply_integer_field(s, s.encoding, raw, "encoding", 0, 255, 1);
    // A style may declare Parent as a plain field; it is consumed by the chain
    // walker, not merged.
}


const RawStyle* find_style(const Document& doc, const std::string& name) {
    for (const RawStyle& style : doc.styles) {
        if (style.name == name) return &style;
    }
    return nullptr;
}

}  // namespace

StyleResolution resolve_style(const Document& doc, const std::string& name) {
    StyleResolution result;
    result.requested = name;
    std::string current = name;
    std::set<std::string> seen;
    std::vector<const RawStyle*> stack;  // leaf-first
    std::vector<std::string> visited_names;  // leaf-first

    while (true) {
        const RawStyle* style = find_style(doc, current);
        if (!style) {
            result.chain.push_back(ChainLink{current, 0, true});
            if (stack.empty()) {
                result.diagnostics.push_back(Diagnostic{
                    Severity::Error, 0, "style-missing",
                    "style '" + sanitize_for_output(name, 60) + "' is not defined", -1});
            } else {
                result.diagnostics.push_back(Diagnostic{
                    Severity::Error, stack.back()->line, "parent-missing",
                    "style '" + sanitize_for_output(stack.back()->name, 60) +
                        "' references missing parent '" + sanitize_for_output(current, 60) + "'",
                    -1});
            }
            break;
        }
        if (seen.count(current)) {
            // Cycle: report the edge that closes the loop and stop. The loop
            // starts at the first occurrence of the repeated style name.
            auto first = std::find(visited_names.begin(), visited_names.end(), current);
            std::string loop;
            for (auto it = first; it != visited_names.end(); ++it) {
                loop += *it;
                loop += " -> ";
            }
            loop += current;
            result.diagnostics.push_back(Diagnostic{
                Severity::Error, style->line, "style-cycle",
                "style inheritance cycle: " + sanitize_for_output(loop, 160), -1});
            break;
        }
        seen.insert(current);
        visited_names.push_back(current);
        result.chain.push_back(ChainLink{current, style->line, false});
        stack.push_back(style);
        if (style->parent.empty()) break;
        current = style->parent;
    }

    // Build the merge order root -> leaf. On cycle/missing-parent the partial
    // chain is still merged so the rest of the report stays meaningful.
    std::vector<const RawStyle*> order(stack.rbegin(), stack.rend());
    // Reverse the chain link list so it reads root -> leaf for the report.
    std::reverse(result.chain.begin(), result.chain.end());

    EffStyle style = builtin_style();
    for (const RawStyle* raw : order) merge_style(style, *raw);
    result.style = style;
    return result;
}

std::vector<Diagnostic> resolve_all_styles(const Document& doc, std::vector<StyleResolution>& out) {
    std::vector<Diagnostic> diagnostics;
    out.clear();
    for (const RawStyle& style : doc.styles) {
        StyleResolution resolution = resolve_style(doc, style.name);
        for (const Diagnostic& d : resolution.diagnostics) diagnostics.push_back(d);
        out.push_back(std::move(resolution));
    }
    return diagnostics;
}

void apply_event_margins(EffStyle& style, const Event& event, int canvas_w, int canvas_h) {
    (void)canvas_w;
    (void)canvas_h;
    struct MarginMapping {
        const char* field;
        int value;
        EffStyle::Integer* slot;
    };
    MarginMapping mappings[] = {
        {"marginl", event.margin_l, &style.margin_l},
        {"marginr", event.margin_r, &style.margin_r},
        {"marginv", event.margin_v, &style.margin_v},
    };
    for (const MarginMapping& m : mappings) {
        if (m.value == 0) continue;  // 0 means inherit from the style
        int clamped = m.value;
        if (clamped < 0) {
            style.adjustments.push_back(Adjustment{m.field, fmt_int(m.value),
                                                   "0 (negative margin clamped)", "event"});
            clamped = 0;
        } else if (clamped > kMarginMax) {
            style.adjustments.push_back(Adjustment{m.field, fmt_int(m.value),
                                                   fmt_int(kMarginMax) + " (clamped)", "event"});
            clamped = kMarginMax;
        }
        m.slot->value = clamped;
        m.slot->source = "event";
        m.slot->cleared = false;
    }
}

}  // namespace sp
