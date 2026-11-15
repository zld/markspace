// main.cpp — CLI entry point and command orchestration.
//
// Exit code contract:
//   0  analysis completed, no overflow
//   2  input or configuration error
//   3  font or output error
//   4  analysis completed but at least one subtitle overflows
#include <cstdio>
#include <string>
#include <vector>

#include "analyzer.hpp"
#include "config.hpp"
#include "font_metrics.hpp"
#include "json_writer.hpp"
#include "subtitle_io.hpp"

namespace {

constexpr int EXIT_OK = 0;
constexpr int EXIT_INPUT_ERROR = 2;
constexpr int EXIT_FONT_OUTPUT_ERROR = 3;
constexpr int EXIT_OVERFLOW = 4;

void print_usage(FILE* out) {
    std::fputs(
        "usage: subtitle-metrics --font FONT --input INPUT --width PX\n"
        "                        [--safe-left PX] [--safe-right PX]\n"
        "                        [--safe-top PX] [--safe-bottom PX]\n"
        "                        [--height PX] [--font-size PX] [--scale N]\n"
        "                        [--wrap space|punctuation|hard]\n"
        "                        [--json-out PATH|-]\n"
        "       subtitle-metrics --make-fixture PATH\n"
        "       subtitle-metrics --help\n",
        out);
}

int exit_for_error(const sm::Error& error) {
    return error.kind == sm::ErrorKind::Config ? EXIT_INPUT_ERROR
                                               : EXIT_FONT_OUTPUT_ERROR;
}

int run_fixture(const sm::Config& cfg) {
    sm::Error error;
    if (!sm::writeFixture(cfg.make_fixture, error)) {
        std::fprintf(stderr, "subtitle-metrics: %s\n",
                     error.message.c_str());
        return EXIT_FONT_OUTPUT_ERROR;
    }
    std::printf("fixture written: %s\n", cfg.make_fixture.c_str());
    return EXIT_OK;
}

int run_analysis(const sm::Config& cfg) {
    sm::Error error;

    std::vector<sm::SubtitleInput> inputs;
    if (!sm::loadSubtitles(cfg.input_path, inputs, error)) {
        std::fprintf(stderr, "subtitle-metrics: %s\n",
                     error.message.c_str());
        return exit_for_error(error);
    }

    sm::FontMetrics font;
    if (!font.open(cfg.font_path, cfg.font_size_px, cfg.scale, error)) {
        std::fprintf(stderr, "subtitle-metrics: %s\n",
                     error.message.c_str());
        return EXIT_FONT_OUTPUT_ERROR;
    }

    sm::Analyzer analyzer(cfg, font);
    sm::AnalysisReport report;
    report.subtitles.reserve(inputs.size());
    for (const auto& item : inputs) {
        report.subtitles.push_back(
            analyzer.analyze(item.id, item.language, item.text));
    }

    for (const auto& s : report.subtitles) {
        report.overflow_count +=
            static_cast<int>(s.overflows.size());
        report.missing_glyph_count += s.missing_glyph_count;
    }
    report.bbox_count = static_cast<int>(report.subtitles.size());

    nlohmann::json summary = sm::buildSummary(report, cfg);
    std::string payload;
    if (!sm::renderSummary(summary, payload, error)) {
        std::fprintf(stderr, "subtitle-metrics: %s\n",
                     error.message.c_str());
        return EXIT_FONT_OUTPUT_ERROR;
    }
    const bool json_to_stdout = (cfg.json_out == "-");
    if (!cfg.json_out.empty() &&
        !sm::writeSummaryAtomic(cfg.json_out, payload, error)) {
        std::fprintf(stderr, "subtitle-metrics: %s\n",
                     error.message.c_str());
        return EXIT_FONT_OUTPUT_ERROR;
    }

    // Human-readable terminal summary. When the JSON itself is streamed
    // to stdout, send the human report to stderr to keep stdout pure.
    FILE* out = json_to_stdout ? stderr : stdout;
    std::fprintf(out, "subtitle_count: %d\n", report.bbox_count);
    std::fprintf(out, "bbox_count: %d\n", report.bbox_count);
    std::fprintf(out, "overflow_count: %d\n",
                 report.overflow_count);
    std::fprintf(out, "missing_glyph_count: %d\n",
                 report.missing_glyph_count);
    for (const auto& s : report.subtitles) {
        std::fprintf(out, "id: %s\n", s.id.c_str());
        std::fprintf(out, "  lines: %zu\n", s.lines.size());
        std::fprintf(out,
                     "  bbox{x: %.2f, y: %.2f, width: %.2f, height: %.2f}\n",
                     s.bbox.x, s.bbox.y, s.bbox.width, s.bbox.height);
        std::fprintf(out, "  overflow_entries: %zu\n",
                     s.overflows.size());
        for (const auto& o : s.overflows) {
            std::fprintf(out,
                         "    - line %d %s %s (%.2fpx)\n",
                         o.line_index, o.direction.c_str(),
                         o.reason.c_str(), o.amount);
        }
        if (!s.missing_glyphs.empty()) {
            std::fprintf(out, "  missing_glyphs: %d\n",
                         s.missing_glyph_count);
        }
    }
    return report.overflow_count > 0 ? EXIT_OVERFLOW : EXIT_OK;
}

}  // namespace

int main(int argc, char** argv) {
    std::vector<std::string> args(argv + 1, argv + argc);
    if (args.empty()) {
        print_usage(stderr);
        return EXIT_INPUT_ERROR;
    }
    for (const auto& a : args) {
        if (a == "--help" || a == "-h") {
            print_usage(stdout);
            return EXIT_OK;
        }
    }

    sm::Config cfg;
    sm::Error error;
    if (!sm::parseArgs(argc, argv, cfg, error) ||
        !sm::validateConfig(cfg, error)) {
        std::fprintf(stderr, "subtitle-metrics: %s\n",
                     error.message.c_str());
        return EXIT_INPUT_ERROR;
    }

    if (!cfg.make_fixture.empty()) return run_fixture(cfg);
    return run_analysis(cfg);
}
