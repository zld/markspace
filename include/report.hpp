#pragma once

#include "access_unit.hpp"

#include <string>

namespace h264 {

// Renders the analysis result as the machine-readable JSON document.  The
// top-level object always contains sps, images, groups and diagnostics so
// downstream pipelines can rely on stable field names.
std::string render_json(const StreamSummary& summary);

// Renders the human-readable terminal report; field semantics and counts
// match the JSON document exactly.
std::string render_text(const StreamSummary& summary);

}  // namespace h264
