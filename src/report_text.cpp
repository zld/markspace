#include "report_text.hpp"

#include <cstdio>
#include <string>

#include "report_support.hpp"

namespace h264inspect {

std::string render_text_report(const AnalysisResult& result) {
    const SummaryCounts counts = summarize(result);
    std::string out;
    char line[512];

    std::snprintf(line, sizeof(line), "h264-inspect structure report\n");
    out += line;
    std::snprintf(line, sizeof(line), "input bytes: %llu, NAL units: %u\n",
                  static_cast<unsigned long long>(result.nal_summary.total_bytes),
                  result.nal_count);
    out += line;

    out += "\nSequence parameter sets:\n";
    if (result.sps_list.empty()) out += "  (none)\n";
    for (const Sps& sps : result.sps_list) {
        std::snprintf(line, sizeof(line),
                      "  sps %u @%llu: %ux%u (coded %ux%u) profile %u level %u "
                      "chroma %u luma %u-bit %s\n",
                      sps.id, static_cast<unsigned long long>(sps.nal_offset),
                      sps.width, sps.height, sps.coded_width, sps.coded_height,
                      sps.profile_idc, sps.level_idc, sps.chroma_format_idc,
                      sps.bit_depth_luma,
                      sps.frame_mbs_only_flag ? "progressive" : "interlaced");
        out += line;
    }

    out += "\nFrames: ";
    std::snprintf(line, sizeof(line),
                  "%zu total, I=%u P=%u B=%u unknown=%u, slices=%u\n",
                  result.frames.size(), counts.i_frames, counts.p_frames,
                  counts.b_frames, counts.unknown_frames, counts.slice_count);
    out += line;
    std::snprintf(line, sizeof(line),
                  "Fields: I=%u P=%u B=%u unknown=%u, pairs=%u isolated=%u\n",
                  counts.i_fields, counts.p_fields, counts.b_fields,
                  counts.unknown_fields, counts.field_pairs,
                  counts.isolated_fields);
    out += line;

    out += "\nImage groups:\n";
    if (result.groups.empty()) out += "  (none)\n";
    for (std::size_t i = 0; i < result.groups.size(); ++i) {
        const GopGroup& g = result.groups[i];
        std::snprintf(line, sizeof(line),
                      "  group %zu @%llu: first=%s boundary=%s frames=%u "
                      "fields=%u slices=%u %s\n",
                      i, static_cast<unsigned long long>(g.start_nal_offset),
                      slice_type_name(g.first_frame_type),
                      boundary_reason_name(g.reason), g.frame_count, g.field_count,
                      g.slice_count, g.complete ? "complete" : "INCOMPLETE");
        out += line;
        if (!g.uncertainty.empty()) {
            out += "    note: " + g.uncertainty + "\n";
        }
    }

    out += "\n";
    std::snprintf(line, sizeof(line),
                  "Exp-Golomb: ue=%llu (fail %llu) se=%llu (fail %llu) "
                  "max_bits ue=%u se=%u max_leading_zeros=%u\n",
                  static_cast<unsigned long long>(result.exp_golomb.ue_calls),
                  static_cast<unsigned long long>(result.exp_golomb.ue_failures),
                  static_cast<unsigned long long>(result.exp_golomb.se_calls),
                  static_cast<unsigned long long>(result.exp_golomb.se_failures),
                  result.exp_golomb.max_ue_bits, result.exp_golomb.max_se_bits,
                  result.exp_golomb.max_leading_zeros);
    out += line;

    out += "\nErrors: " + std::to_string(result.errors.size()) + "\n";
    for (const Diagnostic& d : result.errors) {
        std::snprintf(line, sizeof(line), "  [%s] @%llu: %s\n",
                      error_kind_name(d.kind),
                      static_cast<unsigned long long>(d.offset),
                      d.message.c_str());
        out += line;
    }
    return out;
}

}  // namespace h264inspect
