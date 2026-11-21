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

