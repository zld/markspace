#include "manifest.h"
#include "unicode.h"
#include "font_metrics.h"
#include "line_breaker.h"
#include "timing.h"
#include "exporter.h"

#include <algorithm>
#include <iostream>
#include <string>

int main(int argc, char** argv) {
    if (argc != 4 || std::string(argv[2]) != "--output") {
        std::cerr << "usage: subtitle-metrics MANIFEST.json --output SUMMARY.jsonl\n";
        return 2;
    }
    const std::string output = argv[3];
    Manifest manifest;
    std::vector<ManifestError> errors;
    if (!load_manifest(argv[1], manifest, errors)) {
        for (const ManifestError& error : errors) {
            std::cerr << error.path;
            if (error.item_index >= 0) {
                std::cerr << " (subtitle[" << error.item_index << "]";
                if (!error.item_id.empty()) std::cerr << " " << error.item_id;
                std::cerr << ")";
            }
            std::cerr << ": " << error.message << "\n";
        }
        return 2;
    }
    std::size_t clusters = 0;
    for (const SubtitleItem& item : manifest.items) {
        std::vector<Cluster> shaped;
        std::vector<char32_t> codepoints;
        DecodeError error;
        if (!shape_text(item.text, shaped, error, codepoints)) {
            std::cerr << item.id << ": byte " << error.byte_offset << ": "
                      << error.message << "\n";
            return 3;
        }
        clusters += shaped.size();
    }
    FontMetrics metrics;
    std::string font_error;
    if (!metrics.load(manifest.font.path, manifest.font.size,
                      manifest.font.letter_spacing, manifest.font.line_spacing,
                      font_error)) {
        std::cerr << font_error << "\n";
        return 2;
    }
    const int usable_width =
        manifest.canvas.width - 2 * manifest.canvas.safe_inset;
    int lines = 0;
    int overflows = 0;
    int missing = 0;
    std::vector<SubtitleSummary> summaries;
    for (const SubtitleItem& item : manifest.items) {
        TimingResult timing;
        strip_timing_markers(item.text, timing);
        for (const TimingIssue& issue : timing.issues) {
            std::cerr << item.id << ": byte " << issue.byte_offset << ": "
                      << issue.message << "\n";
        }
        std::vector<Cluster> shaped;
        std::vector<char32_t> codepoints;
        DecodeError error;
        shape_text(timing.stripped, shaped, error, codepoints);
        ShapedText advances = shape_clusters(shaped, codepoints, metrics);
        TextLayout layout = layout_clusters(shaped, advances, metrics,
                                            usable_width, manifest.max_lines);
        lines += static_cast<int>(layout.lines.size());
        overflows += layout.overflow_count;
        missing += advances.missing_glyphs;
        for (const std::string& message : layout.errors) {
            std::cerr << item.id << ": " << message << "\n";
        }
        SubtitleSummary summary;
        summary.id = item.id;
        summary.language = item.language;
        summary.line_count = static_cast<int>(layout.lines.size());
        summary.overflow_count = layout.overflow_count;
        summary.missing_glyphs = advances.missing_glyphs;
        int widest = 0;
        bool first_line = true;
        for (const LineLayout& line : layout.lines) {
            summary.line_ranges.push_back({line.begin_cluster, line.end_cluster});
            summary.break_reasons.push_back(line.break_reason);
            summary.overflow_reasons.push_back(line.overflow_reason);
            widest = std::max(widest, line.width);
            if (!first_line && line.begin_cluster < shaped.size()) {
                summary.line_breaks.push_back(
                    shaped[line.begin_cluster].codepoint_begin);
            }
            first_line = false;
        }
        summary.bbox =
            BBox{0, 0, widest, metrics.block_height(summary.line_count)};
        summary.errors = layout.errors;
        for (const TimingIssue& issue : timing.issues) {
            summary.errors.push_back("byte " + std::to_string(issue.byte_offset) +
                                     ": " + issue.message);
        }
        summaries.push_back(std::move(summary));
    }
    std::string write_error;
    if (!write_summary_atomic(output, summaries, write_error)) {
        std::cerr << "cannot write summary: " << write_error << "\n";
        return 4;
    }
    std::cout << "subtitles=" << manifest.items.size() << " lines=" << lines
              << " overflows=" << overflows << " missing_glyphs=" << missing
              << "\n";
    return 0;
}
