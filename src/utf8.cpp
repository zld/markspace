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

bool inRange(char32_t cp, char32_t lo, char32_t hi) {
    return cp >= lo && cp <= hi;
}

bool isControlCode(char32_t cp) {
    return cp == 0x0D || cp == 0x0A ||
           (cp <= 0x1F) || (cp >= 0x7F && cp <= 0x9F);
}

bool isExtend(char32_t cp) {
    // Nonspacing marks plus variation selectors and common zero-width
    // formatting characters that UAX #29 assigns to Extend.
    if (inRange(cp, 0x0300, 0x036F)) return true;          // combining diacritics
    if (inRange(cp, 0x1AB0, 0x1AFF)) return true;
    if (inRange(cp, 0x1DC0, 0x1DFF)) return true;
    if (inRange(cp, 0x20D0, 0x20FF)) return true;          // combining symbols
    if (inRange(cp, 0xFE00, 0xFE0F)) return true;          // VS1..VS16
    if (cp == 0xFE20 || cp == 0xFE21 ||
        (cp >= 0xFE23 && cp <= 0xFE26)) return true;
    if (inRange(cp, 0xE0100, 0xE01EF)) return true;       // variation supplements
    return false;
}

bool isZwj(char32_t cp) { return cp == 0x200D; }

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

// First segmentation cut: GB3..GB5 (controls/CR/LF incl. merged CRLF),
// GB9 (Extend/ZWJ) and GB9a. Full property tables follow in the next commit.
std::vector<GraphemeCluster>
segmentGraphemes(const std::vector<Codepoint>& cps) {
    std::vector<GraphemeCluster> clusters;
    GraphemeCluster cur;

    auto flush = [&]() {
        if (!cur.cps.empty()) {
            cur.byte_offset = cur.cps.front().byte_offset;
            cur.byte_length = 0;
            for (const auto& c : cur.cps) cur.byte_length += c.byte_length;
            clusters.push_back(cur);
        }
        cur = GraphemeCluster{};
    };

    for (const auto& codepoint : cps) {
        const char32_t cp = codepoint.value;
        bool break_before = true;
        if (!cur.cps.empty()) {
            const char32_t prev = cur.cps.back().value;
            if (prev == 0x0D && cp == 0x0A) {
                break_before = false;                                  // GB3
            } else if (prev == 0x0D || prev == 0x0A ||
                       isControlCode(prev) || cp == 0x0D || cp == 0x0A ||
                       isControlCode(cp)) {
                break_before = true;                                   // GB4/GB5
            } else if (isExtend(cp) || isZwj(cp)) {
                break_before = false;                                  // GB9
            } else if (isExtend(prev)) {
                break_before = false;                                  // GB9a-ish
            }
        }
        if (break_before) flush();
        cur.cps.push_back(codepoint);
        if (cp == 0x0D || cp == 0x0A) cur.is_newline = true;
        if (cp == ' ' || cp == 0x09 || cp == 0x3000) cur.is_space = true;
    }
    flush();
    return clusters;
}

}  // namespace sm
