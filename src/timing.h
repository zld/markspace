#pragma once

#include <cstddef>
#include <string>
#include <vector>

// A problem found while scanning per-character display markers. Offsets refer
// to the original UTF-8 subtitle text, so editors can jump straight to them.
struct TimingIssue {
    std::size_t byte_offset = 0;
    std::string message;
};

struct TimingResult {
    // Text with every marker removed; this is what gets shaped and measured.
    std::string stripped;
    // Number of {t=...} markers that were accepted.
    int marker_count = 0;
    // Time of the last accepted marker, or -1 when there was none.
    long long last_time = -1;
    std::vector<TimingIssue> issues;

    bool ok() const { return issues.empty(); }
};

// Removes "{t=<milliseconds>}" reveal markers without contributing to the
// measured width. Markers are processed in order and their times must be
// non-decreasing. Unknown markers, unterminated markers (missing '}') and
// backwards times are reported by byte offset but never stop the scan, so one
// bad marker cannot hide later problems or corrupt the following subtitle.
// "{{" and "}}" are literal braces.
void strip_timing_markers(const std::string& text, TimingResult& out);
