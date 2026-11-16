// subtitle-audit: event normalization into standalone render inputs.
#pragma once

#include <string>
#include <vector>

#include "document.hpp"
#include "style_graph.hpp"

namespace subaudit {

// One Dialogue event rewritten as a self-contained ASS document: styles
// flattened (no Parent, no "~"), event interval rebased to [0, duration)
// following the left-closed/right-open rule, measurement at the midpoint.
struct LayoutInput {
    int event_id = 0;
    long long start_ms = 0;
    long long duration_ms = 0;
    long long mid_ms = 0;
    std::string ass;  // standalone ASS document rendering this single event
};

// Builds one LayoutInput per Dialogue event (comments are skipped).
// Fails if an event references an unknown style.
bool build_layout_inputs(const Document& doc, const std::vector<ResolvedStyle>& styles,
                         std::vector<LayoutInput>& out, std::string& err);

// Formats milliseconds as "h:mm:ss.cc".
std::string format_ass_time(long long ms);

}  // namespace subaudit
