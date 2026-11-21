#pragma once

#include "font_metrics.h"
#include "unicode.h"

#include <cstddef>
#include <string>
#include <vector>

struct LineLayout {
    // Cluster range [begin_cluster, end_cluster); trailing whitespace is not
    // included, so widths match what is visibly drawn.
    std::size_t begin_cluster = 0;
    std::size_t end_cluster = 0;
    int width = 0;
    // "space", "punctuation", "fallback", "explicit" or "end".
    std::string break_reason;
    // Empty when the line fits; otherwise "cluster_too_wide" or "max_lines".
    std::string overflow_reason;
};

struct TextLayout {
    std::vector<LineLayout> lines;
    // Number of lines that could not be satisfied within the constraints.
    int overflow_count = 0;
    // Widest visible line, in pixels.
    int max_line_width = 0;
    // Item-level, locatable messages (cluster index in brackets).
    std::vector<std::string> errors;
};

struct ShapedText {
    // advances[i] is the drawn width of cluster i plus one trailing letter
    // spacing, so the width of [b, e) is sum(advances[b..e)) - spacing.
    std::vector<long long> advances;
    // Number of code points that had to be substituted with .notdef.
    int missing_glyphs = 0;
    // Distinct missing code points in text order, formatted as "U+XXXX".
    std::vector<std::string> missing_codepoints;
    std::vector<std::string> errors;
};

// Resolves every code point against the face, applying letter spacing between
// clusters. Default-ignorable code points that the face lacks still occupy
// zero width so their absence does not inflate a line.
ShapedText shape_clusters(const std::vector<Cluster>& clusters,
                          const std::vector<char32_t>& codepoints,
                          const FontMetrics& metrics);

// Greedy line breaking over shaped clusters. Break opportunities are the
// whitespace and punctuation boundaries reported by unicode.cpp; when a run
// has no such boundary the breaker falls back to cluster boundaries. A single
// cluster wider than `max_width` is emitted on its own and recorded as an
// overflow. Exceeding `max_lines` is reported once per subtitle.
TextLayout layout_clusters(const std::vector<Cluster>& clusters,
                           const ShapedText& shaped, const FontMetrics& metrics,
                           int max_width, int max_lines);

// True when the greedy breaker prefers this boundary: it follows whitespace
// or punctuation, and the next cluster is neither extending nor a newline.
bool is_preferred_break(const Cluster& previous, const Cluster& next);
