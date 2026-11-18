// JSON rendering of an analysis result.
#pragma once

#include <string>

#include "report.hpp"

namespace h264inspect {

std::string render_json_report(const AnalysisResult& result);

}  // namespace h264inspect
