#include "report_json.hpp"

#include <cstdio>
#include <string>

#include "report_support.hpp"

namespace h264inspect {

std::string render_json_report(const AnalysisResult& result) {
    const SummaryCounts counts = summarize(result);
    std::string out;
    out.reserve(4096);

    out += "{\n";

    // --- Parameter sets ---------------------------------------------------
    out += "  \"sps\": [\n";
    for (std::size_t i = 0; i < result.sps_list.size(); ++i) {
        const Sps& sps = result.sps_list[i];
        out += "    {\n";
        out += "      \"id\": " + std::to_string(sps.id) + ",\n";
        out += "      \"profile_idc\": " + std::to_string(sps.profile_idc) + ",\n";
        out += "      \"level_idc\": " + std::to_string(sps.level_idc) + ",\n";
        out += "      \"chroma_format_idc\": " +
               std::to_string(sps.chroma_format_idc) + ",\n";
        out += "      \"bit_depth_luma\": " + std::to_string(sps.bit_depth_luma) +
               ",\n";
        out += "      \"bit_depth_chroma\": " +
               std::to_string(sps.bit_depth_chroma) + ",\n";
        out += "      \"width\": " + std::to_string(sps.width) + ",\n";
        out += "      \"height\": " + std::to_string(sps.height) + ",\n";
        out += "      \"coded_width\": " + std::to_string(sps.coded_width) + ",\n";
        out += "      \"coded_height\": " + std::to_string(sps.coded_height) + ",\n";
        out += std::string("      \"frame_mbs_only_flag\": ") +
               (sps.frame_mbs_only_flag ? "true" : "false") + ",\n";
        out += std::string("      \"interlaced\": ") +
               (sps.interlaced ? "true" : "false") + ",\n";
        out += "      \"nal_offset\": " + std::to_string(sps.nal_offset) + "\n";
        out += i + 1 == result.sps_list.size() ? "    }\n" : "    },\n";
    }
    out += "  ],\n";

    out += "  \"pps\": [\n";
    for (std::size_t i = 0; i < result.pps_list.size(); ++i) {
        const Pps& pps = result.pps_list[i];
        out += "    {\"id\": " + std::to_string(pps.id) +
               ", \"sps_id\": " + std::to_string(pps.sps_id) +
               ", \"entropy_coding_mode\": " +
               (pps.entropy_coding_mode ? "true" : "false") +
               ", \"nal_offset\": " + std::to_string(pps.nal_offset) + "}" +
               (i + 1 == result.pps_list.size() ? "\n" : ",\n");
    }
    out += "  ],\n";

    // --- Frame and field statistics ---------------------------------------
    out += "  \"frames\": {\n";
    out += "    \"total\": " + std::to_string(result.frames.size()) + ",\n";
    out += "    \"i\": " + std::to_string(counts.i_frames) + ",\n";
    out += "    \"p\": " + std::to_string(counts.p_frames) + ",\n";
    out += "    \"b\": " + std::to_string(counts.b_frames) + ",\n";
    out += "    \"unknown\": " + std::to_string(counts.unknown_frames) + ",\n";
    out += "    \"pictures\": " + std::to_string(counts.picture_count) + ",\n";
    out += "    \"slices\": " + std::to_string(counts.slice_count) + ",\n";
    out += "    \"fields\": {\n";
    out += "      \"total\": " + std::to_string(counts.i_fields + counts.p_fields +
                                                counts.b_fields +
                                                counts.unknown_fields) +
           ",\n";
    out += "      \"i\": " + std::to_string(counts.i_fields) + ",\n";
    out += "      \"p\": " + std::to_string(counts.p_fields) + ",\n";
    out += "      \"b\": " + std::to_string(counts.b_fields) + ",\n";
    out += "      \"unknown\": " + std::to_string(counts.unknown_fields) + ",\n";
    out += "      \"pairs\": " + std::to_string(counts.field_pairs) + ",\n";
    out += "      \"isolated\": " + std::to_string(counts.isolated_fields) + "\n";
    out += "    },\n";
    out += "    \"items\": [\n";
    for (std::size_t i = 0; i < result.frames.size(); ++i) {
        const FrameInfo& frame = result.frames[i];
        out += "      {\"index\": " + std::to_string(i) +
               ", \"type\": " + json_quote(slice_type_name(frame.type)) +
               ", \"idr\": " + (frame.is_idr ? "true" : "false") +
               ", \"field_coded\": " + (frame.field_coded ? "true" : "false") +
               ", \"field_pair\": " + (frame.field_pair ? "true" : "false") +
               ", \"isolated_field\": " +
               (frame.isolated_field ? "true" : "false") +
               ", \"bottom_field\": " + (frame.bottom_field ? "true" : "false") +
               ", \"reference\": " + (frame.reference ? "true" : "false") +
               ", \"first_mb_min\": " + std::to_string(frame.first_mb_min) +
               ", \"first_mb_max\": " + std::to_string(frame.first_mb_max) +
               ", \"frame_num\": " + std::to_string(frame.frame_num) +
               ", \"slices\": " + std::to_string(frame.slice_count) +
               ", \"offset\": " + std::to_string(frame.first_nal_offset) +
               ", \"errors\": " + (frame.had_errors ? "true" : "false") +
               (frame.uncertainty.empty()
                    ? std::string()
                    : ", \"uncertainty\": " + json_quote(frame.uncertainty)) +
               "}" + (i + 1 == result.frames.size() ? "\n" : ",\n");
    }
    out += "    ]\n";
    out += "  },\n";

    // --- Image groups ------------------------------------------------------
    out += "  \"groups\": [\n";
    for (std::size_t i = 0; i < result.groups.size(); ++i) {
        const GopGroup& group = result.groups[i];
        out += "    {\n";
        out += "      \"index\": " + std::to_string(i) + ",\n";
        out += "      \"start_nal_offset\": " +
               std::to_string(group.start_nal_offset) + ",\n";
        out += "      \"first_frame_type\": " +
               json_quote(slice_type_name(group.first_frame_type)) + ",\n";
        out += "      \"boundary\": " + json_quote(boundary_reason_name(group.reason)) +
               ",\n";
        out += "      \"frames\": " + std::to_string(group.frame_count) + ",\n";
        out += "      \"fields\": " + std::to_string(group.field_count) + ",\n";
        out += "      \"slices\": " + std::to_string(group.slice_count) + ",\n";
        out += "      \"complete\": " + std::string(group.complete ? "true" : "false") +
               ",\n";
        out += "      \"uncertain\": " +
               std::string(group.uncertain ? "true" : "false") +
               (group.uncertainty.empty()
                    ? std::string()
                    : ",\n      \"reason_detail\": " + json_quote(group.uncertainty)) +
               "\n";
        out += i + 1 == result.groups.size() ? "    }\n" : "    },\n";
    }
    out += "  ],\n";

    // --- Exp-Golomb statistics --------------------------------------------
    const ExpGolombStats& stats = result.exp_golomb;
    out += "  \"exp_golomb\": {\n";
    out += "    \"ue_calls\": " + std::to_string(stats.ue_calls) + ",\n";
    out += "    \"se_calls\": " + std::to_string(stats.se_calls) + ",\n";
    out += "    \"ue_failures\": " + std::to_string(stats.ue_failures) + ",\n";
    out += "    \"se_failures\": " + std::to_string(stats.se_failures) + ",\n";
    out += "    \"max_ue_bits\": " + std::to_string(stats.max_ue_bits) + ",\n";
    out += "    \"max_se_bits\": " + std::to_string(stats.max_se_bits) + ",\n";
    out += "    \"max_leading_zeros\": " +
           std::to_string(stats.max_leading_zeros) + ",\n";
    out += "    \"max_ue_value\": " + std::to_string(stats.max_ue_value) + ",\n";
    out += "    \"max_se_magnitude\": " + std::to_string(stats.max_se_magnitude) +
           "\n";
    out += "  },\n";

    // --- NAL scan summary --------------------------------------------------
    const NalScanSummary& nal = result.nal_summary;
    out += "  \"nal_scan\": {\n";
    out += "    \"nal_units\": " + std::to_string(result.nal_count) + ",\n";
    out += "    \"total_bytes\": " + std::to_string(nal.total_bytes) + ",\n";
    out += "    \"leading_garbage_bytes\": " +
           std::to_string(nal.leading_garbage_bytes) + ",\n";
    out += "    \"leading_zero_bytes\": " +
           std::to_string(nal.leading_zero_bytes) + ",\n";
    out += "    \"consecutive_start_codes\": " +
           std::to_string(nal.consecutive_start_codes) + ",\n";
    out += "    \"empty_nal_units\": " +
           std::to_string(nal.empty_nal_units) + ",\n";
    out += "    \"inter_nal_zero_bytes\": " +
           std::to_string(nal.inter_nal_zero_bytes) + ",\n";
    out += "    \"trailing_zero_bytes\": " +
           std::to_string(nal.trailing_zero_bytes) + "\n";
    out += "  },\n";

    // --- Diagnostics -------------------------------------------------------
    if (result.errors.empty()) {
        out += "  \"errors\": []\n";
    } else {
        out += "  \"errors\": [\n";
        for (std::size_t i = 0; i < result.errors.size(); ++i) {
            const Diagnostic& d = result.errors[i];
            out += "    {\"kind\": " + json_quote(error_kind_name(d.kind)) +
                   ", \"offset\": " + std::to_string(d.offset) +
                   ", \"message\": " + json_quote(d.message) + "}" +
                   (i + 1 == result.errors.size() ? "\n" : ",\n");
        }
        out += "  ]\n";
    }
    out += "}\n";
    return out;
}

}  // namespace h264inspect
