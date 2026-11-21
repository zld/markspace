#include "exporter.h"
#include "fixtures.h"
#include "font_metrics.h"
#include "line_breaker.h"
#include "manifest.h"
#include "timing.h"
#include "unicode.h"

#include <algorithm>
#include <cstdio>
#include <fstream>
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
    std::string font;
    int width = 0;
    int safe_inset = -1;
    int max_lines = 0;
    std::string missing_glyph;
    bool collect_errors = false;
    std::string make_fixture;
};

void usage(std::ostream& out) {
    out << "usage:\n"
        << "  subtitle-metrics --make-fixture FILE\n"
        << "  subtitle-metrics measure --input FILE --output FILE [options]\n"
        << "options:\n"
        << "  --font FILE            font used for metrics\n"
        << "  --width N              canvas width in pixels\n"
        << "  --safe-inset N         horizontal inset on both sides\n"
        << "  --max-lines N          maximum lines per subtitle\n"
        << "  --missing-glyph MODE   error (default) or fallback\n"
        << "  --collect-errors       keep going after item errors\n"
        << "  --strict               stop at the first item error (default)\n";
}

bool parse_int(const std::string& text, int min_value, int max_value, int& out) {
    if (text.empty()) {
        return false;
    }
    long long value = 0;
    for (const char c : text) {
        if (c < '0' || c > '9') {
            return false;
        }
        value = value * 10 + (c - '0');
        if (value > max_value) {
            return false;
        }
    }
    if (value < min_value) {
        return false;
    }
    out = static_cast<int>(value);
    return true;
}

bool take_value(int argc, char** argv, int& index, const std::string& flag,
                std::string& target, std::string& error) {
    if (index + 1 >= argc) {
        error = flag + " needs a value";
        return false;
    }
    target = argv[++index];
    return true;
}

bool parse_options(int argc, char** argv, Options& options, std::string& error) {
    if (argc < 2) {
        error = "missing command";
        return false;
    }
    int index = 1;
    const std::string command = argv[index++];
    if (command == "--help" || command == "-h") {
        usage(std::cout);
        std::exit(kOk);
    }
    if (command == "--make-fixture") {
        if (index >= argc) {
            error = "--make-fixture needs an output path";
            return false;
        }
        options.make_fixture = argv[index];
        return true;
    }
    if (command != "measure") {
        error = "unknown command: " + command;
        return false;
    }
    for (; index < argc; ++index) {
        const std::string flag = argv[index];
        std::string raw;
        if (flag == "--collect-errors") {
            options.collect_errors = true;
        } else if (flag == "--strict") {
            options.collect_errors = false;
        } else if (flag == "--input") {
            if (!take_value(argc, argv, index, flag, options.input, error)) return false;
        } else if (flag == "--output") {
            if (!take_value(argc, argv, index, flag, options.output, error)) return false;
        } else if (flag == "--font") {
            if (!take_value(argc, argv, index, flag, options.font, error)) return false;
        } else if (flag == "--missing-glyph") {
            if (!take_value(argc, argv, index, flag, raw, error)) return false;
            if (raw != "error" && raw != "fallback") {
                error = "--missing-glyph must be error or fallback";
                return false;
            }
            options.missing_glyph = raw;
        } else if (flag == "--width") {
            if (!take_value(argc, argv, index, flag, raw, error)) return false;
            if (!parse_int(raw, 1, 1000000, options.width)) {
                error = "--width must be a positive integer";
                return false;
            }
        } else if (flag == "--safe-inset") {
            if (!take_value(argc, argv, index, flag, raw, error)) return false;
            if (!parse_int(raw, 0, 1000000, options.safe_inset)) {
                error = "--safe-inset must be a non-negative integer";
                return false;
            }
        } else if (flag == "--max-lines") {
            if (!take_value(argc, argv, index, flag, raw, error)) return false;
            if (!parse_int(raw, 1, 100, options.max_lines)) {
                error = "--max-lines must be an integer between 1 and 100";
                return false;
            }
        } else {
            error = "unknown option: " + flag;
            return false;
        }
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

    // Command-line values override the manifest for the fields they name.
    if (!options.font.empty()) manifest.font.path = options.font;
    if (options.width > 0) manifest.canvas.width = options.width;
    if (options.safe_inset >= 0) manifest.canvas.safe_inset = options.safe_inset;
    if (options.max_lines > 0) manifest.max_lines = options.max_lines;
    if (!options.missing_glyph.empty()) {
        manifest.font.missing_glyph = options.missing_glyph;
    }
    const int usable_width =
        manifest.canvas.width - 2 * manifest.canvas.safe_inset;
    if (manifest.font.path.empty() || manifest.font.size <= 0 ||
        manifest.canvas.width <= 0 || manifest.canvas.safe_inset < 0 ||
        usable_width <= 0 || manifest.max_lines <= 0 ||
        (manifest.font.missing_glyph != "error" &&
         manifest.font.missing_glyph != "fallback")) {
        std::cerr << "invalid font or layout parameters\n";
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

    // Collect mode is explicit: any item error makes the run report it and
    // keep going. Without the flag the run is strict and stops at the first
    // bad subtitle, leaving an existing summary untouched.
    const bool strict = !options.collect_errors;
    std::vector<SubtitleSummary> summaries;
    int total_lines = 0;
    int total_overflows = 0;
    int total_missing = 0;
    int total_errors = 0;
    bool aborted = false;

    auto fail_item = [&](std::size_t index, const std::string& id,
                         const std::string& message, SubtitleSummary& summary) {
        ++total_errors;
        summary.errors.push_back(message);
        std::cerr << "subtitle[" << index << "] (" << id << "): " << message
                  << "\n";
        if (strict) {
            aborted = true;
            return false;
        }
        return true;
    };

    for (std::size_t index = 0; index < manifest.items.size() && !aborted;
         ++index) {
        const SubtitleItem& item = manifest.items[index];
        SubtitleSummary summary;
        summary.id = item.id;
        summary.language = item.language;

        TimingResult timing;
        strip_timing_markers(item.text, timing);
        for (const TimingIssue& issue : timing.issues) {
            if (!fail_item(index, item.id,
                           "byte " + std::to_string(issue.byte_offset) + ": " +
                               issue.message,
                           summary)) {
                break;
            }
        }
        if (aborted) break;

        std::vector<Cluster> clusters;
        std::vector<char32_t> codepoints;
        DecodeError decode_error;
        if (!shape_text(timing.stripped, clusters, decode_error, codepoints)) {
            fail_item(index, item.id,
                      "byte " + std::to_string(decode_error.byte_offset) +
                          ": invalid UTF-8 (" + decode_error.message + ")",
                      summary);
            if (aborted) break;
        }

        ShapedText shaped = shape_clusters(clusters, codepoints, metrics);
        for (const std::string& error : shaped.errors) {
            if (!fail_item(index, item.id, error, summary)) break;
        }
        if (aborted) break;
        if (!shaped.missing_codepoints.empty() &&
            manifest.font.missing_glyph == "error") {
            std::string list;
            for (const std::string& label : shaped.missing_codepoints) {
                list += (list.empty() ? "" : ", ") + label;
            }
            if (!fail_item(index, item.id, "font has no glyph for " + list,
                           summary)) {
                break;
            }
        }

        TextLayout layout = layout_clusters(clusters, shaped, metrics,
                                            usable_width, manifest.max_lines);
        for (const std::string& error : layout.errors) {
            if (!fail_item(index, item.id, error, summary)) break;
        }
        if (aborted) break;

        summary.line_count = static_cast<int>(layout.lines.size());
        summary.overflow_count = layout.overflow_count;
        summary.missing_glyphs = shaped.missing_glyphs;
        int widest = 0;
        bool first_line = true;
        for (const LineLayout& line : layout.lines) {
            LineRange range{line.begin_cluster, line.end_cluster};
            summary.line_ranges.push_back(range);
            summary.break_reasons.push_back(line.break_reason);
            if (!line.overflow_reason.empty()) {
                summary.overflow_reasons.push_back(line.overflow_reason);
            }
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

    if (aborted) {
        // Strict mode keeps the previous summary on disk untouched.
        std::cout << "subtitles=" << summaries.size() << " lines=" << total_lines
                  << " overflows=" << total_overflows
                  << " missing_glyphs=" << total_missing
                  << " errors=" << total_errors << "\n";
        return kItemErrors;
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

bool write_fixture(const std::string& path) {
    std::ofstream out(path, std::ios::binary | std::ios::trunc);
    if (!out) {
        std::cerr << "cannot write fixture: " << path << "\n";
        return false;
    }
    out << make_fixture_json();
    out.flush();
    if (!out) {
        std::cerr << "cannot write fixture: " << path << "\n";
        return false;
    }
    std::cout << "fixture=" << path << "\n";
    return true;
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
    if (!options.make_fixture.empty()) {
        return write_fixture(options.make_fixture) ? kOk : kWriteFailure;
    }
    return run_measure(options);
}
