// Terminal report generation: human-readable text or JSON.
#pragma once

#include <string>

#include "access_unit.hpp"

namespace h264inspect {

enum class ReportFormat { Text, Json };

struct SummaryCounts {
    std::uint32_t i_frames = 0;
    std::uint32_t p_frames = 0;
    std::uint32_t b_frames = 0;
    std::uint32_t unknown_frames = 0;
    std::uint32_t i_fields = 0;
    std::uint32_t p_fields = 0;
    std::uint32_t b_fields = 0;
    std::uint32_t unknown_fields = 0;
    std::uint32_t field_pairs = 0;
    std::uint32_t isolated_fields = 0;
    std::uint32_t picture_count = 0;
    std::uint32_t group_count = 0;
    std::uint32_t slice_count = 0;
    std::uint32_t nal_count = 0;
};

SummaryCounts summarize(const AnalysisResult& result);

std::string render_report(const AnalysisResult& result, ReportFormat format);

}  // namespace h264inspect
