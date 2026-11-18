#include "report.hpp"

#include <string>

#include "report_json.hpp"
#include "report_text.hpp"

namespace h264inspect {

namespace {

void count_frame(SummaryCounts& counts, SliceType type) {
    switch (type) {
        case SliceType::I: ++counts.i_frames; break;
        case SliceType::P: ++counts.p_frames; break;
        case SliceType::B: ++counts.b_frames; break;
        default: ++counts.unknown_frames; break;
    }
}

void count_field(SummaryCounts& counts, SliceType type) {
    switch (type) {
        case SliceType::I: ++counts.i_fields; break;
        case SliceType::P: ++counts.p_fields; break;
        case SliceType::B: ++counts.b_fields; break;
        default: ++counts.unknown_fields; break;
    }
}

}  // namespace



SummaryCounts summarize(const AnalysisResult& result) {
    SummaryCounts counts;
    counts.picture_count = static_cast<std::uint32_t>(result.pictures.size());
    counts.group_count = static_cast<std::uint32_t>(result.groups.size());
    counts.nal_count = result.nal_count;
    for (const FrameInfo& frame : result.frames) {
        count_frame(counts, frame.type);
        if (frame.field_coded) {
            // A frame assembled from fields contributes two field entries;
            // an isolated field contributes one.
            const int fields_per_frame = frame.field_pair ? 2 : 1;
            for (int i = 0; i < fields_per_frame; ++i) {
                count_field(counts, frame.type);
            }
            if (frame.field_pair) {
                ++counts.field_pairs;
            } else {
                ++counts.isolated_fields;
            }
        }
        counts.slice_count += frame.slice_count;
    }
    return counts;
}

std::string render_report(const AnalysisResult& result, ReportFormat format) {
    return format == ReportFormat::Json ? render_json_report(result)
                                        : render_text_report(result);
}

}  // namespace h264inspect
