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


}  // namespace sp
