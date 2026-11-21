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

enum class ClusterKind {
    // Whitespace that can be consumed at a line break but is not itself
    // visible at the end of a line.
    Whitespace,
    // A whitespace character that must stay inside the line (for example a
    // no-break space or a kept space).
    HardSpace,
    Punctuation,
    Other,
};

struct Cluster {
    char32_t first = 0;
    std::size_t byte_begin = 0;
    std::size_t byte_end = 0;
    // Index of the first code point of the cluster in the decoded list.
    std::size_t codepoint_begin = 0;
    // Number of code points in the cluster (1 or more).
    std::size_t codepoint_count = 0;
    ClusterKind kind = ClusterKind::Other;
    // Reserved for the shaping step; not serialized.
    char32_t reserved = 0;
};

// Decodes and clusters in one step. On a UTF-8 error the byte offset is
// returned in `error` and `out` keeps the clusters decoded so far.
bool shape_text(const std::string& utf8, std::vector<Cluster>& out,
                DecodeError& error, std::vector<char32_t>& codepoints);

// Groups code points into user-perceived clusters. Base characters absorb
// combining marks, variation selectors, emoji modifiers, enclosing marks and
// zero-width-joiner sequences so a line break never splits them.
void build_clusters(const std::vector<CodePoint>& codepoints,
                    std::vector<Cluster>& out);


// True when a cluster only extends the one before it (combining mark,
// variation selector, emoji modifier, keycap mark or joiner) and therefore
// must not start a line.
bool is_extending_cluster(const Cluster& cluster);

// True for LRM/RLM/bidi controls and the zero-width space.
bool is_format_control(char32_t codepoint);

// Default-ignorable code points (variation selectors, joiners, bidi controls,
// soft hyphen). They never draw, even when the face has no glyph for them.
bool is_default_ignorable(char32_t codepoint);

// True when the boundary before `next` may legally host a line break.
// `previous` is the cluster immediately to its left, or nullptr at the start
// of a run.
bool can_break_before(const Cluster* previous, char32_t next, bool next_is_zwj);
