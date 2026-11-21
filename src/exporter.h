#pragma once

#include <cstddef>
#include <string>
#include <vector>

struct BBox {
    int x = 0;
    int y = 0;
    int width = 0;
    int height = 0;
};

struct LineRange {
    std::size_t begin_cluster = 0;
    std::size_t end_cluster = 0;
};

// Everything the exporter needs for one subtitle, already laid out.
struct SubtitleSummary {
    std::string id;
    std::string language;
    int line_count = 0;
    BBox bbox;
    // Code-point offsets in the stripped text where each line after the first
    // starts.
    std::vector<std::size_t> line_breaks;
    std::vector<LineRange> line_ranges;
    std::vector<std::string> break_reasons;
    std::vector<std::string> overflow_reasons;
    int overflow_count = 0;
    int missing_glyphs = 0;
    std::vector<std::string> errors;
};

// Appends one JSONL record (no trailing newline). Field order is fixed so the
// output is byte-for-byte reproducible.
std::string summary_to_json(const SubtitleSummary& summary);

// Writes all records to `path` through a same-directory temporary file and an
// atomic rename. On failure the previous file at `path` is left untouched and
// `error` explains why.
bool write_summary_atomic(const std::string& path,
                          const std::vector<SubtitleSummary>& summaries,
                          std::string& error);

std::string json_escape(const std::string& value);
