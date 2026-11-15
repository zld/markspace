#pragma once

#include <cstddef>
#include <string>
#include <vector>

#include "config.hpp"
#include "font_metrics.hpp"
#include "line_breaker.hpp"
#include "utf8.hpp"

namespace sm {

struct OverflowEntry {
    int line_index = 0;             // 0-based line within the subtitle
    std::string direction;          // "left" | "right" | "top" | "bottom"
    std::string reason;             // cluster_too_wide | no_break_opportunity
    double amount = 0.0;            // pixels beyond the safe edge
};

struct SubtitleLine {
    int line_index = 0;
    std::string text;               // UTF-8 text of the line
    std::size_t first_cluster = 0;  // global cluster index in the subtitle
    std::size_t cluster_count = 0;
    double width = 0.0;             // measured pen width
    double x = 0.0, y = 0.0;        // bbox origin relative to subtitle box
};

struct BBox {
    double x = 0.0;
    double y = 0.0;
    double width = 0.0;
    double height = 0.0;
};

struct SubtitleResult {
    std::string id;
    std::string language;           // empty when the input omits it
    std::string sha256_hex;         // hash of the original text bytes
    BBox bbox;
    std::vector<SubtitleLine> lines;
    std::vector<OverflowEntry> overflows;
    std::vector<char32_t> missing_glyphs;
    int missing_glyph_count = 0;
};

struct AnalysisReport {
    std::vector<SubtitleResult> subtitles;
    int overflow_count = 0;
    int missing_glyph_count = 0;
    int bbox_count = 0;
};

// Runs the full per-subtitle pipeline on already-segmented raw input.
// Missing glyphs are recoverable: analysis continues and the codepoints
// are recorded. Font/config failures are reported before calling this.
class Analyzer {
public:
    Analyzer(const Config& cfg, FontMetrics& font);

    SubtitleResult analyze(const std::string& id,
                           const std::string& language,
                           const std::string& text) const;

private:
    const Config& cfg_;
    FontMetrics& font_;
    double content_width_;
};

// SHA-256 of raw bytes, rendered as lowercase hex.
std::string sha256Hex(const std::string& data);

}  // namespace sm
