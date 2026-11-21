#include "exporter.h"
#include "font_metrics.h"
#include "line_breaker.h"
#include "manifest.h"
#include "timing.h"
#include "unicode.h"

#include <algorithm>
#include <iostream>
#include <string>
#include <vector>

namespace {

enum ExitCode {
    kOk = 0,
    kInvalidInput = 2,
    kItemErrors = 3,
    kWriteFailure = 4,
};

struct Options {
    std::string input;
    std::string output;
};

void usage(std::ostream& out) {
    out << "usage:\n"
        << "  subtitle-metrics measure --input FILE --output FILE\n";
}

bool parse_options(int argc, char** argv, Options& options, std::string& error) {
    if (argc < 2 || std::string(argv[1]) != "measure") {
        error = "expected the measure command";
        return false;
    }
    for (int index = 2; index < argc; ++index) {
        const std::string flag = argv[index];
        if (flag != "--input" && flag != "--output") {
            error = "unknown option: " + flag;
            return false;
        }
        if (index + 1 >= argc) {
            error = flag + " needs a value";
            return false;
        }
        (flag == "--input" ? options.input : options.output) = argv[++index];
    }
    if (options.input.empty()) {
        error = "--input is required";
        return false;
    }
    if (options.output.empty()) {
        error = "--output is required";
        return false;
    }
    return true;
}

int run_measure(const Options& options) {
    Manifest manifest;
    std::vector<ManifestError> manifest_errors;
    load_manifest(options.input, manifest, manifest_errors);
    if (!manifest_errors.empty()) {
        for (const ManifestError& error : manifest_errors) {
            std::cerr << error.path;
            if (error.item_index >= 0) {
                std::cerr << " (subtitle[" << error.item_index << "]";
                if (!error.item_id.empty()) std::cerr << " " << error.item_id;
                std::cerr << ")";
            }
            std::cerr << ": " << error.message << "\n";
        }
        return kInvalidInput;
    }

    FontMetrics metrics;
    std::string font_error;
    if (!metrics.load(manifest.font.path, manifest.font.size,
                      manifest.font.letter_spacing, manifest.font.line_spacing,
                      font_error)) {
        std::cerr << "$.font.path: " << font_error << "\n";
        return kInvalidInput;
    }
    const int usable_width =
        manifest.canvas.width - 2 * manifest.canvas.safe_inset;
    if (usable_width <= 0) {
        std::cerr << "$.canvas: safe inset leaves no usable text width\n";
        return kInvalidInput;
    }

    std::vector<SubtitleSummary> summaries;
    int total_lines = 0;
    int total_overflows = 0;
    int total_missing = 0;
    int total_errors = 0;
    for (std::size_t index = 0; index < manifest.items.size(); ++index) {
        const SubtitleItem& item = manifest.items[index];
        SubtitleSummary summary;
        summary.id = item.id;
        summary.language = item.language;

        TimingResult timing;
        strip_timing_markers(item.text, timing);
        for (const TimingIssue& issue : timing.issues) {
            ++total_errors;
            summary.errors.push_back("byte " + std::to_string(issue.byte_offset) +
                                     ": " + issue.message);
            std::cerr << "subtitle[" << index << "] (" << item.id
                      << "): byte " << issue.byte_offset << ": "
                      << issue.message << "\n";
        }
        std::vector<Cluster> clusters;
        std::vector<char32_t> codepoints;
        DecodeError decode_error;
        if (!shape_text(timing.stripped, clusters, decode_error, codepoints)) {
            ++total_errors;
            const std::string message =
                "byte " + std::to_string(decode_error.byte_offset) +
                ": invalid UTF-8 (" + decode_error.message + ")";
            summary.errors.push_back(message);
            std::cerr << "subtitle[" << index << "] (" << item.id << "): "
                      << message << "\n";
        }
        ShapedText shaped = shape_clusters(clusters, codepoints, metrics);
        if (!shaped.missing_codepoints.empty() &&
            manifest.font.missing_glyph == "error") {
            std::string list;
            for (const std::string& label : shaped.missing_codepoints) {
                list += (list.empty() ? "" : ", ") + label;
            }
            ++total_errors;
            summary.errors.push_back("font has no glyph for " + list);
        }
        TextLayout layout = layout_clusters(clusters, shaped, metrics,
                                            usable_width, manifest.max_lines);
        for (const std::string& message : layout.errors) {
            ++total_errors;
            summary.errors.push_back(message);
            std::cerr << "subtitle[" << index << "] (" << item.id << "): "
                      << message << "\n";
        }
        summary.line_count = static_cast<int>(layout.lines.size());
        summary.overflow_count = layout.overflow_count;
        summary.missing_glyphs = shaped.missing_glyphs;
        int widest = 0;
        bool first_line = true;
        for (const LineLayout& line : layout.lines) {
            summary.line_ranges.push_back({line.begin_cluster, line.end_cluster});
            summary.break_reasons.push_back(line.break_reason);
            summary.overflow_reasons.push_back(line.overflow_reason);
            widest = std::max(widest, line.width);
            if (!first_line && line.begin_cluster < clusters.size()) {
                summary.line_breaks.push_back(
                    clusters[line.begin_cluster].codepoint_begin);
            }
            first_line = false;
        }
        summary.bbox =
            BBox{0, 0, widest, metrics.block_height(summary.line_count)};
        total_lines += summary.line_count;
        if (summary.overflow_count > 0) ++total_overflows;
        if (summary.missing_glyphs > 0) ++total_missing;
        summaries.push_back(std::move(summary));
    }

    std::string write_error;
    if (!write_summary_atomic(options.output, summaries, write_error)) {
        std::cerr << "cannot write summary: " << write_error << "\n";
        return kWriteFailure;
    }
    std::cout << "subtitles=" << summaries.size() << " lines=" << total_lines
              << " overflows=" << total_overflows
              << " missing_glyphs=" << total_missing
              << " errors=" << total_errors << "\n";
    return total_errors > 0 ? kItemErrors : kOk;
}

}  // namespace

int main(int argc, char** argv) {
    Options options;
    std::string error;
    if (!parse_options(argc, argv, options, error)) {
        std::cerr << "error: " << error << "\n\n";
        usage(std::cerr);
        return kInvalidInput;
    }
    return run_measure(options);
}
