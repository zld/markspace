#include "utf8.hpp"

#include <array>

namespace sm {

namespace {

// UTF-8 leading byte -> expected continuation length; 0 marks an illegal
// leading byte (continuation byte 0x80..0xBF or 0xF8+).
constexpr std::array<int, 256> lead_length_table() {
    std::array<int, 256> t{};
    for (int i = 0; i < 256; ++i) {
        if (i < 0x80) {
            t[i] = 1;
        } else if ((i & 0xE0) == 0xC0) {
            t[i] = 2;
        } else if ((i & 0xF0) == 0xE0) {
            t[i] = 3;
        } else if ((i & 0xF8) == 0xF0) {
            t[i] = 4;
        } else {
            t[i] = 0;
        }
    }
    return t;
}

constexpr auto kLeadLength = lead_length_table();

}  // namespace

bool decodeUtf8(const std::string& src, std::vector<Codepoint>& out,
                Utf8Error& error) {
    const auto* p = reinterpret_cast<const unsigned char*>(src.data());
    const std::size_t n = src.size();
    std::size_t i = 0;
    while (i < n) {
                const int len = kLeadLength[p[i]];
        if (len == 0) {
            error.byte_offset = i;
            error.message = "invalid UTF-8 leading byte";
            return false;
        }
        if (n - i < static_cast<std::size_t>(len)) {
            error.byte_offset = i;
            error.message = "truncated UTF-8 sequence";
            return false;
        }
        char32_t cp = 0;
        switch (len) {
            case 1:
                cp = p[i];
                break;
            case 2:
                cp = p[i] & 0x1Fu;
                break;
            case 3:
                cp = p[i] & 0x0Fu;
                break;
            default:
                cp = p[i] & 0x07u;
                break;
        }
        for (int k = 1; k < len; ++k) {
            unsigned char c = p[i + k];
            if ((c & 0xC0u) != 0x80u) {
                error.byte_offset = i + k;
                error.message = "invalid UTF-8 continuation byte";
                return false;
            }
            cp = (cp << 6) | (c & 0x3Fu);
        }
        // Reject overlong encodings, UTF-16 surrogates and out-of-range.
        const char32_t min_cp = len == 1 ? 0x0u
                                 : len == 2 ? 0x80u
                                 : len == 3 ? 0x800u : 0x10000u;
        if (cp < min_cp || cp > 0x10FFFFu ||
            (cp >= 0xD800u && cp <= 0xDFFFu)) {
            error.byte_offset = i;
            error.message = "invalid UTF-8 scalar value";
            return false;
        }
        out.push_back(Codepoint{cp, i, len});
        i += static_cast<std::size_t>(len);
    }
    return true;
}

void appendUtf8(std::string& dst, char32_t cp) {
    if (cp < 0x80) {
        dst.push_back(static_cast<char>(cp));
    } else if (cp < 0x800) {
        dst.push_back(static_cast<char>(0xC0 | (cp >> 6)));
        dst.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
    } else if (cp < 0x10000) {
        dst.push_back(static_cast<char>(0xE0 | (cp >> 12)));
        dst.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
        dst.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
    } else {
        dst.push_back(static_cast<char>(0xF0 | (cp >> 18)));
        dst.push_back(static_cast<char>(0x80 | ((cp >> 12) & 0x3F)));
        dst.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
        dst.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
    }
}

std::string clusterText(const GraphemeCluster& cluster) {
    std::string s;
    for (const auto& c : cluster.cps) appendUtf8(s, c.value);
    return s;
}

// ---------------------------------------------------------------------------
// Grapheme cluster break properties (UAX #29, GB1..GB13 + GB999).
// ---------------------------------------------------------------------------

namespace {

bool inRange(char32_t cp, char32_t lo, char32_t hi) {
    return cp >= lo && cp <= hi;
}

enum class Gcb { Other, Control, CR, LF, Extend, ZWJ, RegionalIndicator,
                 Prepend, SpacingMark, L, V, T, LV, LVT, VT,
                 ExtendedPictographic };

bool isControlCode(char32_t cp) {
    return (cp <= 0x1F && cp != 0x0A && cp != 0x0D) ||
           (cp >= 0x7F && cp <= 0x9F);
}

bool isExtend(char32_t cp) {
    if (inRange(cp, 0x0300, 0x036F)) return true;          // combining marks
    if (inRange(cp, 0x1AB0, 0x1AFF)) return true;
    if (inRange(cp, 0x1DC0, 0x1DFF)) return true;
    if (inRange(cp, 0x20D0, 0x20FF)) return true;          // combining symbols
    if (inRange(cp, 0xFE00, 0xFE0F)) return true;          // VS1..VS16
    if (cp == 0xFE20 || cp == 0xFE21 ||
        (cp >= 0xFE23 && cp <= 0xFE26)) return true;       // half marks
    if (inRange(cp, 0xE0100, 0xE01EF)) return true;       // variation suppl.
    if (inRange(cp, 0x1F3FB, 0x1F3FF)) return true;       // emoji modifiers
    return false;
}

bool isSpacingMark(char32_t cp) {
    if (inRange(cp, 0x0903, 0x0903)) return true;          // common Devanagari
    if (inRange(cp, 0x093B, 0x093C)) return true;
    if (inRange(cp, 0x093E, 0x0940)) return true;
    if (cp == 0x302E || cp == 0x302F) return true;
    if (inRange(cp, 0x1160, 0x11FF)) return true;          // jamo v/t
    if (inRange(cp, 0x1B00, 0x1B04)) return true;
    return false;
}

bool isPrepend(char32_t cp) {
    return cp == 0x0600 || cp == 0x0601 || cp == 0x0602 ||
           cp == 0x0603 || cp == 0x0604 || cp == 0x0605 ||
           cp == 0x061C || cp == 0x200E || cp == 0x200F ||
           inRange(cp, 0x202A, 0x202E) ||
           inRange(cp, 0x2066, 0x2069);
}

bool isRegionalIndicator(char32_t cp) {
    return inRange(cp, 0x1F1E6, 0x1F1FF);
}

struct Range { char32_t lo, hi; };

// Extended_Pictographic block ranges (Emoji 15.0 level coverage).
constexpr Range kPictoRanges[] = {
    {0x00A9, 0x00A9}, {0x00AE, 0x00AE}, {0x203C, 0x203C},
    {0x2049, 0x2049}, {0x2122, 0x2122}, {0x2139, 0x2139},
    {0x2194, 0x2199}, {0x21A9, 0x21AA}, {0x231A, 0x231B},
    {0x2328, 0x2328}, {0x23CF, 0x23CF}, {0x23E9, 0x23F3},
    {0x23F8, 0x23FA}, {0x24C2, 0x24C2}, {0x25AA, 0x25AB},
    {0x25B6, 0x25B6}, {0x25C0, 0x25C0}, {0x25FB, 0x25FE},
    {0x2600, 0x2604}, {0x260E, 0x260E}, {0x2611, 0x2611},
    {0x2614, 0x2615}, {0x2618, 0x2618}, {0x261D, 0x261D},
    {0x2620, 0x2620}, {0x2622, 0x2623}, {0x2626, 0x2626},
    {0x262A, 0x262A}, {0x262E, 0x262F}, {0x2638, 0x263A},
    {0x2640, 0x2640}, {0x2642, 0x2642}, {0x2648, 0x2653},
    {0x265F, 0x2660}, {0x2663, 0x2663}, {0x2665, 0x2666},
    {0x2668, 0x2668}, {0x267B, 0x267B}, {0x267E, 0x267F},
    {0x2692, 0x2697}, {0x2699, 0x2699}, {0x269B, 0x269C},
    {0x26A0, 0x26A1}, {0x26A7, 0x26A7}, {0x26AA, 0x26AB},
    {0x26B0, 0x26B1}, {0x26BD, 0x26BE}, {0x26C4, 0x26C5},
    {0x26C8, 0x26C8}, {0x26CE, 0x26CF}, {0x26D1, 0x26D1},
    {0x26D3, 0x26D4}, {0x26E9, 0x26EA}, {0x26F0, 0x26F5},
    {0x26F7, 0x26FA}, {0x26FD, 0x26FD}, {0x2702, 0x2702},
    {0x2705, 0x2705}, {0x2708, 0x270D}, {0x270F, 0x2710},
    {0x2712, 0x2712}, {0x2714, 0x2714}, {0x2716, 0x2716},
    {0x271D, 0x271D}, {0x2721, 0x2721}, {0x2728, 0x2728},
    {0x2733, 0x2734}, {0x2744, 0x2744}, {0x2747, 0x2747},
    {0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755},
    {0x2757, 0x2757}, {0x2763, 0x2764}, {0x2795, 0x2797},
    {0x27A1, 0x27A1}, {0x27B0, 0x27B0}, {0x27BF, 0x27BF},
    {0x2934, 0x2935}, {0x2B05, 0x2B07}, {0x2B1B, 0x2B1C},
    {0x2B50, 0x2B50}, {0x2B55, 0x2B55}, {0x3030, 0x3030},
    {0x3297, 0x3297}, {0x3299, 0x3299}, {0x1F000, 0x1F0FF},
    {0x1F10D, 0x1F10F}, {0x1F12F, 0x1F12F},
    {0x1F16C, 0x1F171}, {0x1F17E, 0x1F17F},
    {0x1F18E, 0x1F18E}, {0x1F191, 0x1F19A},
    {0x1F1AD, 0x1F1E5}, {0x1F201, 0x1F20F},
    {0x1F21A, 0x1F21A}, {0x1F22F, 0x1F22F},
    {0x1F232, 0x1F23A}, {0x1F23C, 0x1F23F},
    {0x1F249, 0x1F3FA}, {0x1F400, 0x1F53D},
    {0x1F546, 0x1F64F}, {0x1F680, 0x1F6FF},
    {0x1F774, 0x1F77F}, {0x1F7D5, 0x1F7FF},
    {0x1F80C, 0x1F80F}, {0x1F848, 0x1F84F},
    {0x1F85A, 0x1F85F}, {0x1F888, 0x1F88F},
    {0x1F8AE, 0x1F8FF}, {0x1F90C, 0x1F93A},
    {0x1F93C, 0x1F945}, {0x1F947, 0x1FAFF},
    {0x1FC00, 0x1FFFD},
};

bool isExtendedPictographic(char32_t cp) {
    for (const auto& r : kPictoRanges) {
        if (cp < r.lo) return false;  // table is sorted
        if (cp <= r.hi) return true;
    }
    return false;
}

Gcb gcbProperty(char32_t cp) {
    if (cp == 0x0D) return Gcb::CR;
    if (cp == 0x0A) return Gcb::LF;
    if (isControlCode(cp)) return Gcb::Control;
    if (cp == 0x200D) return Gcb::ZWJ;
    if (isRegionalIndicator(cp)) return Gcb::RegionalIndicator;
    if (inRange(cp, 0x1100, 0x115F) || cp == 0xA960) return Gcb::L;
    if (inRange(cp, 0x1160, 0x11A7)) return Gcb::V;
    if (inRange(cp, 0x11A8, 0x11FF)) return Gcb::T;
    if (inRange(cp, 0xAC00, 0xD7A3)) {
        const int lvt = static_cast<int>(cp - 0xAC00);
        if (lvt % 28 != 0) return Gcb::LVT;
        return (lvt / 28) % 21 == 0 ? Gcb::LV : Gcb::VT;
    }
    if (isSpacingMark(cp)) return Gcb::SpacingMark;
    if (isPrepend(cp)) return Gcb::Prepend;
    if (isExtendedPictographic(cp)) return Gcb::ExtendedPictographic;
    if (isExtend(cp)) return Gcb::Extend;
    return Gcb::Other;
}

}  // namespace

std::vector<GraphemeCluster>
segmentGraphemes(const std::vector<Codepoint>& cps) {
    std::vector<GraphemeCluster> clusters;
    GraphemeCluster cur;
    int ri_count = 0;  // RI clusters already absorbed into current cluster

    auto flush = [&]() {
        if (!cur.cps.empty()) {
            cur.byte_offset = cur.cps.front().byte_offset;
            cur.byte_length = 0;
            for (const auto& c : cur.cps) cur.byte_length += c.byte_length;
            clusters.push_back(cur);
        }
        cur = GraphemeCluster{};
        ri_count = 0;
    };

    for (const auto& codepoint : cps) {
        const char32_t cp = codepoint.value;
        const Gcb cur_prop = gcbProperty(cp);
        bool break_before = true;

        if (!cur.cps.empty()) {
            const GraphemeCluster& prev_cluster = cur;
            const Gcb prev_prop = gcbProperty(prev_cluster.cps.back().value);

            if (prev_prop == Gcb::CR && cur_prop == Gcb::LF) {
                break_before = false;                                   // GB3
            } else if (prev_prop == Gcb::Control || prev_prop == Gcb::CR ||
                       prev_prop == Gcb::LF || cur_prop == Gcb::Control ||
                       cur_prop == Gcb::CR || cur_prop == Gcb::LF) {
                break_before = true;                                    // GB4/GB5
            } else if (prev_prop == Gcb::L &&
                       (cur_prop == Gcb::L || cur_prop == Gcb::V ||
                        cur_prop == Gcb::LV || cur_prop == Gcb::LVT)) {
                break_before = false;                                   // GB6
            } else if ((prev_prop == Gcb::LV || prev_prop == Gcb::V) &&
                       (cur_prop == Gcb::V || cur_prop == Gcb::T)) {
                break_before = false;                                   // GB7
            } else if ((prev_prop == Gcb::LVT || prev_prop == Gcb::T) &&
                       cur_prop == Gcb::T) {
                break_before = false;                                   // GB8
            } else if (cur_prop == Gcb::Extend || cur_prop == Gcb::ZWJ) {
                break_before = false;                                   // GB9
            } else if (cur_prop == Gcb::SpacingMark) {
                break_before = false;                                   // GB9a
            } else if (prev_prop == Gcb::Prepend) {
                break_before = false;                                   // GB9b
            } else if (prev_prop == Gcb::ZWJ &&
                       cur_prop == Gcb::ExtendedPictographic) {
                bool pict_start = false;
                for (const auto& c : prev_cluster.cps) {
                    Gcb p = gcbProperty(c.value);
                    if (p == Gcb::ExtendedPictographic) {
                        pict_start = true;
                        break;
                    }
                    if (p != Gcb::Extend) break;
                }
                break_before = !pict_start;                             // GB11
            } else if (prev_prop == Gcb::RegionalIndicator &&
                       cur_prop == Gcb::RegionalIndicator &&
                       ri_count % 2 == 1) {
                break_before = false;                                   // GB12/GB13
            } else {
                break_before = true;                                    // GB999
            }
        }

        if (break_before) flush();
        cur.cps.push_back(codepoint);
        if (cur_prop == Gcb::RegionalIndicator) ++ri_count;
        if (cp == 0x0D || cp == 0x0A) cur.is_newline = true;
        if (cp == ' ' || cp == 0x09 || cp == 0x3000) cur.is_space = true;
    }
    flush();
    return clusters;
}

}  // namespace sm
