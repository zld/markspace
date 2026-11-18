// Access-unit segmentation and image-group aggregation.
#pragma once

#include <cstdint>

#include "analysis_result.hpp"

namespace h264inspect {

// Analyzes an Annex B elementary stream. Never aborts on malformed NALs; each
// failure is recorded with its byte offset and analysis continues.
AnalysisResult analyze_stream(const ByteSource& source);

}  // namespace h264inspect
