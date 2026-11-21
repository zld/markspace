#include "unicode.h"

#include <CoreFoundation/CoreFoundation.h>

#include <algorithm>

namespace {

bool is_nonbase(char32_t cp) {
    return CFCharacterSetIsLongCharacterMember(
        CFCharacterSetGetPredefined(kCFCharacterSetNonBase),
        static_cast<UTF32Char>(cp));
}

bool is_punctuation(char32_t cp) {
    return CFCharacterSetIsLongCharacterMember(
        CFCharacterSetGetPredefined(kCFCharacterSetPunctuation),
        static_cast<UTF32Char>(cp));
}

bool is_whitespace(char32_t cp) {
    return CFCharacterSetIsLongCharacterMember(
        CFCharacterSetGetPredefined(kCFCharacterSetWhitespaceAndNewline),
        static_cast<UTF32Char>(cp));
}

bool is_variation_selector(char32_t cp) {
    return (cp >= 0xFE00 && cp <= 0xFE0F) || (cp >= 0xE0100 && cp <= 0xE01EF);
}

bool is_emoji_modifier(char32_t cp) {
    return cp >= 0x1F3FB && cp <= 0x1F3FF;
}

bool is_regional_indicator(char32_t cp) {
    return cp >= 0x1F1E6 && cp <= 0x1F1FF;
}

// Code points that attach to the preceding cluster without carrying their own
// advance: variation selectors, emoji modifiers, enclosing keycap marks and
// the zero-width joiner itself. The joiners are handled by the caller because
// they also pull in the following cluster.
bool is_extending(char32_t cp) {
    return is_nonbase(cp) || is_variation_selector(cp) ||
           is_emoji_modifier(cp) || cp == 0x20E3 || cp == 0x200C;
}

// Control characters and line separators are always their own cluster so the
// parser can report or handle them deterministically.
bool is_separator(char32_t cp) {
    return cp == '\n' || cp == '\r' || cp == '\t' || cp == 0x0B || cp == 0x0C ||
           cp == 0x85 || cp == 0x2028 || cp == 0x2029 || cp < 0x20;
}

}  // namespace

bool decode_utf8(const std::string& text, std::vector<CodePoint>& out,
                 DecodeError& error) {
    out.clear();
    std::size_t i = 0;
    const std::size_t size = text.size();
    while (i < size) {
        const unsigned char lead = static_cast<unsigned char>(text[i]);
        if (lead < 0x80) {
            out.push_back(CodePoint{static_cast<char32_t>(lead), i, i + 1});
            ++i;
            continue;
        }

        std::size_t length = 0;
        char32_t value = 0;
        char32_t minimum = 0;
        if (lead >= 0xC2 && lead <= 0xDF) {
            length = 2;
            value = lead & 0x1F;
            minimum = 0x80;
        } else if (lead >= 0xE0 && lead <= 0xEF) {
            length = 3;
            value = lead & 0x0F;
            minimum = 0x800;
        } else if (lead >= 0xF0 && lead <= 0xF4) {
            length = 4;
            value = lead & 0x07;
            minimum = 0x10000;
        } else if (lead >= 0x80 && lead <= 0xBF) {
            error = DecodeError{i, "isolated UTF-8 continuation byte"};
            return false;
        } else {
            error = DecodeError{i, "invalid UTF-8 lead byte"};
            return false;
        }

        if (i + length > size) {
            error = DecodeError{i, "truncated UTF-8 sequence"};
            return false;
        }
        for (std::size_t k = 1; k < length; ++k) {
            const unsigned char next = static_cast<unsigned char>(text[i + k]);
            if ((next & 0xC0) != 0x80) {
                error = DecodeError{i, "invalid UTF-8 continuation byte"};
                return false;
            }
            value = (value << 6) | (next & 0x3F);
        }
        if (value < minimum) {
            error = DecodeError{i, "overlong UTF-8 encoding"};
            return false;
        }
        if (value >= 0xD800 && value <= 0xDFFF) {
            error = DecodeError{i, "UTF-8 encodes a surrogate code point"};
            return false;
        }
        if (value > 0x10FFFF) {
            error = DecodeError{i, "UTF-8 code point above U+10FFFF"};
            return false;
        }
        out.push_back(CodePoint{static_cast<char32_t>(value), i, i + length});
        i += length;
    }
    return true;
}

void build_clusters(const std::vector<CodePoint>& codepoints,
                    std::vector<Cluster>& out) {
    out.clear();
    std::size_t i = 0;
    while (i < codepoints.size()) {
        Cluster cluster;
        cluster.first = codepoints[i].value;
        cluster.byte_begin = codepoints[i].byte_begin;
        cluster.byte_end = codepoints[i].byte_end;
        cluster.codepoint_begin = i;
        cluster.codepoint_count = 1;
        ++i;

        if (is_separator(cluster.first)) {
            // A CRLF pair forms one visual cluster.
            if (cluster.first == '\r' && i < codepoints.size() &&
                codepoints[i].value == '\n') {
                cluster.byte_end = codepoints[i].byte_end;
                ++cluster.codepoint_count;
                ++i;
            }
        } else {
            while (i < codepoints.size()) {
                // Extenders always attach, including after a joiner.
                if (is_extending(codepoints[i].value)) {
                    cluster.byte_end = codepoints[i].byte_end;
                    ++cluster.codepoint_count;
                    ++i;
                    continue;
                }
                // A joiner pulls in the base that follows it, so emoji ZWJ
                // sequences such as a family emoji stay in one cluster.
                if (codepoints[i].value == 0x200D) {
                    cluster.byte_end = codepoints[i].byte_end;
                    ++cluster.codepoint_count;
                    ++i;
                    if (i < codepoints.size() &&
                        !is_separator(codepoints[i].value)) {
                        cluster.byte_end = codepoints[i].byte_end;
                        ++cluster.codepoint_count;
                        ++i;
                    }
                    continue;
                }
                // Regional-indicator flags are exactly pairs.
                if (is_regional_indicator(cluster.first) &&
                    cluster.codepoint_count == 1 &&
                    is_regional_indicator(codepoints[i].value)) {
                    cluster.byte_end = codepoints[i].byte_end;
                    ++cluster.codepoint_count;
                    ++i;
                    continue;
                }
                break;
            }
        }

        if (is_separator(cluster.first) && !is_whitespace(cluster.first)) {
            cluster.kind = ClusterKind::Other;
        } else if (cluster.first == 0x00A0 || cluster.first == 0x202F ||
                   cluster.first == 0x2060) {
            cluster.kind = ClusterKind::HardSpace;
        } else if (is_whitespace(cluster.first)) {
            cluster.kind = ClusterKind::Whitespace;
        } else if (is_punctuation(cluster.first) || cluster.first == 0x3000) {
            cluster.kind = ClusterKind::Punctuation;
        } else {
            cluster.kind = ClusterKind::Other;
        }
        out.push_back(cluster);
    }
}

bool shape_text(const std::string& utf8, std::vector<Cluster>& out,
                DecodeError& error, std::vector<char32_t>& codepoints) {
    std::vector<CodePoint> decoded;
    codepoints.clear();
    if (!decode_utf8(utf8, decoded, error)) {
        out.clear();
        decode_utf8(utf8.substr(0, error.byte_offset), decoded, error);
        build_clusters(decoded, out);
        return false;
    }
    codepoints.reserve(decoded.size());
    for (const CodePoint& cp : decoded) {
        codepoints.push_back(cp.value);
    }
    build_clusters(decoded, out);
    return true;
}

bool is_extending_cluster(const Cluster& cluster) {
    if (cluster.codepoint_count == 0) {
        return false;
    }
    return is_extending(cluster.first) || is_regional_indicator(cluster.first);
}

bool is_format_control(char32_t codepoint) {
    return codepoint == 0x200B || codepoint == 0x200E || codepoint == 0x200F ||
           codepoint == 0x202A || codepoint == 0x202B || codepoint == 0x202C ||
           codepoint == 0x202D || codepoint == 0x202E || codepoint == 0x2066 ||
           codepoint == 0x2067 || codepoint == 0x2068 || codepoint == 0x2069;
}

bool is_default_ignorable(char32_t codepoint) {
    return is_variation_selector(codepoint) || is_format_control(codepoint) ||
           codepoint == 0x00AD || codepoint == 0x034F || codepoint == 0x200C ||
           codepoint == 0x200D || codepoint == 0x2060 || codepoint == 0x2061 ||
           codepoint == 0x2062 || codepoint == 0x2063 || codepoint == 0x2064 ||
           (codepoint >= 0x180B && codepoint <= 0x180D) ||
           (codepoint >= 0xFE00 && codepoint <= 0xFE0F);
}

bool can_break_before(const Cluster* previous, char32_t next,
                      bool next_is_zwj) {
    (void)next;
    if (previous == nullptr) {
        return false;
    }
    if (next_is_zwj) {
        return false;
    }
    return previous->kind == ClusterKind::Whitespace ||
           previous->kind == ClusterKind::Punctuation;
}
