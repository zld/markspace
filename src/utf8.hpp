#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace sm {

struct Codepoint {
    char32_t value = 0;
    std::size_t byte_offset = 0;  // offset into the source UTF-8 bytes
    int byte_length = 0;          // 1..4
};

struct Utf8Error {
    std::size_t byte_offset = 0;
    std::string message;
};

// Strict UTF-8 decoder. Never inserts replacement characters and never
// skips bytes: overlong encodings, surrogates, truncated sequences and
// values above U+10FFFF are reported as an error at the offending offset.
bool decodeUtf8(const std::string& src, std::vector<Codepoint>& out,
                Utf8Error& error);

struct GraphemeCluster {
    std::vector<Codepoint> cps;
    std::size_t byte_offset = 0;  // offset of the first codepoint
    int byte_length = 0;          // total UTF-8 bytes of the cluster
    bool is_newline = false;      // CR, LF or a merged CRLF cluster
    bool is_space = false;        // whitespace break opportunity
};

// Segment codepoints into extended grapheme clusters. Combining marks,
// variation selectors and ZWJ emoji sequences are kept inside one cluster
// and can therefore never be split by the line breaker.
std::vector<GraphemeCluster>
segmentGraphemes(const std::vector<Codepoint>& cps);

// Re-encode one codepoint as UTF-8.
void appendUtf8(std::string& dst, char32_t cp);

// Re-encode a full cluster as UTF-8.
std::string clusterText(const GraphemeCluster& cluster);

}  // namespace sm
