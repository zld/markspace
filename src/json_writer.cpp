#include "json_writer.hpp"

#include <cstdio>
#include <fstream>
#include <unistd.h>
#include <sys/stat.h>

#include <algorithm>
#include <cstdlib>

namespace sm {

using json = nlohmann::json;

namespace {

// Lowercase 4/5/6-hex-digit codepoint form used for missing glyph labels.
std::string codepointHex(char32_t cp) {
    static const char* digits = "0123456789abcdef";
    std::string s = "U+";
    bool started = false;
    for (int shift = 20; shift >= 0; shift -= 4) {
        const int nibble = (cp >> shift) & 0xF;
        if (nibble != 0 || started || shift == 0) {
            s.push_back(digits[nibble]);
            started = true;
        }
    }
    return s;
}

json bboxJson(const BBox& b) {
    return json{{"x", b.x},
                {"y", b.y},
                {"width", b.width},
                {"height", b.height}};
}

}  // namespace

json buildSummary(const AnalysisReport& report, const Config& cfg) {
    json subtitles = json::array();
    for (const SubtitleResult& s : report.subtitles) {
        json lines = json::array();
        for (const SubtitleLine& l : s.lines) {
            lines.push_back({
                {"line_index", l.line_index},
                {"text", l.text},
                {"first_cluster", l.first_cluster},
                {"cluster_count", l.cluster_count},
                {"width", l.width},
                {"x", l.x},
                {"y", l.y},
            });
        }

        json overflows = json::array();
        // Stable ordering: line index, then direction, then reason.
        std::vector<OverflowEntry> sorted_of = s.overflows;
        std::sort(sorted_of.begin(), sorted_of.end(),
                  [](const OverflowEntry& a, const OverflowEntry& b) {
                      if (a.line_index != b.line_index)
                          return a.line_index < b.line_index;
                      if (a.direction != b.direction)
                          return a.direction < b.direction;
                      return a.reason < b.reason;
                  });
        for (const OverflowEntry& o : sorted_of) {
            overflows.push_back({
                {"line_index", o.line_index},
                {"direction", o.direction},
                {"reason", o.reason},
                {"amount", o.amount},
            });
        }

        json missing = json::array();
        for (char32_t cp : s.missing_glyphs)
            missing.push_back(codepointHex(cp));

        subtitles.push_back({
            {"id", s.id},
            {"language", s.language},
            {"sha256", s.sha256_hex},
            {"bbox", bboxJson(s.bbox)},
            {"lines", lines},
            {"cluster_total",
             s.lines.empty()
                 ? 0
                 : s.lines.back().first_cluster +
                       s.lines.back().cluster_count},
            {"missing_glyphs", missing},
            {"missing_glyph_count", s.missing_glyph_count},
            {"overflow_entries", overflows},
        });
    }

    SummaryTotals totals;
    totals.subtitle_count = static_cast<int>(report.subtitles.size());
    totals.bbox_count = totals.subtitle_count;
    totals.overflow_count = report.overflow_count;
    totals.missing_glyph_count = report.missing_glyph_count;

    return {
        {"tool", "subtitle-metrics"},
        {"version", "1.0.0"},
        {"parameters",
         {
             {"canvas_width", cfg.width},
             {"canvas_height", cfg.height},
             {"safe_left", cfg.safe_left},
             {"safe_right", cfg.safe_right},
             {"safe_top", cfg.safe_top},
             {"safe_bottom", cfg.safe_bottom},
             {"font_size_px", cfg.font_size_px},
             {"scale", cfg.scale},
             {"wrap", wrapPolicyName(cfg.wrap)},
         }},
        {"summary",
         {
             {"subtitle_count", totals.subtitle_count},
             {"bbox_count", totals.bbox_count},
             {"overflow_count", totals.overflow_count},
             {"missing_glyph_count", totals.missing_glyph_count},
         }},
        {"subtitles", subtitles},
    };
}

bool renderSummary(const json& summary, std::string& out, Error& error) {
    try {
        out = summary.dump(2);
    } catch (const std::exception& e) {
        error = {ErrorKind::Output,
                 std::string("failed to serialize summary: ") + e.what()};
        return false;
    }
    if (static_cast<long>(out.size()) > kMaxOutputBytes) {
        error = {ErrorKind::Output,
                 "summary output exceeds limit of " +
                     std::to_string(kMaxOutputBytes) + " bytes"};
        return false;
    }
    return true;
}

bool writeSummaryAtomic(const std::string& path,
                        const std::string& payload, Error& error) {
    if (path == "-") {
        std::fwrite(payload.data(), 1, payload.size(), stdout);
        if (std::fflush(stdout) != 0) {
            error = {ErrorKind::Output, "failed writing to stdout"};
            return false;
        }
        return true;
    }

    // Sibling temporary so the final rename is on the same filesystem.
    const std::string tmp = path + ".tmp." +
                            std::to_string(::getpid());
    {
        std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
        if (!out) {
            error = {ErrorKind::Output,
                     "cannot open temporary output: " + tmp};
            return false;
        }
        out.write(payload.data(), static_cast<std::streamsize>(
                                      payload.size()));
        out.flush();
        if (!out) {
            error = {ErrorKind::Output,
                     "failed writing temporary output: " + tmp};
            std::remove(tmp.c_str());
            return false;
        }
    }
    if (std::rename(tmp.c_str(), path.c_str()) != 0) {
        error = {ErrorKind::Output,
                 "atomic rename failed for: " + path};
        std::remove(tmp.c_str());
        return false;
    }
    // Restrict created file permissions to user read/write plus group
    // read; never create world-writable artifacts.
    ::chmod(path.c_str(), S_IRUSR | S_IWUSR | S_IRGRP);
    return true;
}

}  // namespace sm
