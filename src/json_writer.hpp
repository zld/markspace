#pragma once

#include <string>

#include <nlohmann/json.hpp>

#include "analyzer.hpp"
#include "config.hpp"

namespace sm {

struct SummaryTotals {
    int subtitle_count = 0;
    int bbox_count = 0;
    int overflow_count = 0;
    int missing_glyph_count = 0;
};

// Builds the canonical, deterministically ordered summary document.
// Subtitles keep input order; keys are emitted in a fixed order and
// nested arrays (lines, overflows, missing glyphs) are sorted or kept
// in their computed stable order.
nlohmann::json buildSummary(const AnalysisReport& report,
                            const Config& cfg);

// Renders the summary as pretty JSON text and enforces kMaxOutputBytes.
bool renderSummary(const nlohmann::json& summary, std::string& out,
                   Error& error);

// Writes to `path` via a sibling temporary file followed by an atomic
// rename, or to stdout when path is "-". Failures are ErrorKind::Output.
bool writeSummaryAtomic(const std::string& path,
                        const std::string& payload, Error& error);

}  // namespace sm
