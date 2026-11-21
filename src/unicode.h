#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

// One Unicode code point decoded from UTF-8, tagged with the byte offsets of
// its UTF-8 encoding inside the original subtitle text.
struct CodePoint {
    char32_t value = 0;
    std::size_t byte_begin = 0;
    std::size_t byte_end = 0;
};

struct DecodeError {
    std::size_t byte_offset = 0;
    std::string message;
};

// Strict UTF-8 decoding. Rejects overlong encodings, isolated continuation
// bytes, truncated sequences, surrogates and code points above U+10FFFF.
bool decode_utf8(const std::string& text, std::vector<CodePoint>& out,
                 DecodeError& error);
