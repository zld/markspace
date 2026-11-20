#include "h264/analyzer.hpp"

#include <sstream>

namespace h264 {

namespace {

void write_json_string(std::ostringstream& out, const std::string& value) {
  out << '"';
  for (const char c : value) {
    switch (c) {
      case '"': out << "\\\""; break;
      case '\\': out << "\\\\"; break;
      case '\n': out << "\\n"; break;
      case '\t': out << "\\t"; break;
      case '\r': out << "\\r"; break;
      default:
        if (static_cast<unsigned char>(c) < 0x20) {
          const char* hex = "0123456789abcdef";
          out << "\\u00" << hex[(c >> 4) & 0xF] << hex[c & 0xF];
        } else {
          out << c;
        }
    }
  }
  out << '"';
}

void write_json_sps(std::ostringstream& out, const Sps& sps) {
  out << "{\"sps_id\":" << sps.sps_id
      << ",\"profile_idc\":" << sps.profile_idc
      << ",\"level_idc\":" << sps.level_idc
      << ",\"chroma_format_idc\":" << sps.chroma_format_idc
      << ",\"bit_depth_luma\":" << (sps.bit_depth_luma_minus8 + 8)
      << ",\"bit_depth_chroma\":" << (sps.bit_depth_chroma_minus8 + 8)
      << ",\"pic_width_in_mbs\":" << sps.pic_width_in_mbs
      << ",\"pic_height_in_map_units\":" << sps.pic_height_in_map_units
      << ",\"width\":" << sps.width_pixels()
      << ",\"height\":" << sps.height_pixels()
      << ",\"frame_mbs_only_flag\":" << (sps.frame_mbs_only_flag ? 1 : 0)
      << ",\"mb_adaptive_frame_field_flag\":"
      << (sps.mb_adaptive_frame_field_flag ? 1 : 0)
      << ",\"log2_max_frame_num\":" << sps.log2_max_frame_num
      << ",\"pic_order_cnt_type\":" << sps.pic_order_cnt_type
      << ",\"log2_max_pic_order_cnt_lsb\":"
      << sps.log2_max_pic_order_cnt_lsb
      << ",\"delta_pic_order_always_zero_flag\":"
      << (sps.delta_pic_order_always_zero_flag ? 1 : 0)
      << ",\"max_num_ref_frames\":" << sps.max_num_ref_frames
      << ",\"frame_cropping_flag\":" << (sps.frame_cropping_flag ? 1 : 0)
      << ",\"vui_parameters_present_flag\":"
      << (sps.vui_parameters_present_flag ? 1 : 0) << "}";
}

void write_json_pps(std::ostringstream& out, const Pps& pps) {
  out << "{\"pps_id\":" << pps.pps_id << ",\"sps_id\":" << pps.sps_id
      << ",\"entropy_coding_mode_flag\":"
      << (pps.entropy_coding_mode_flag ? 1 : 0)
      << ",\"bottom_field_pic_order_in_frame_present_flag\":"
      << (pps.bottom_field_pic_order_in_frame_present_flag ? 1 : 0)
      << ",\"weighted_pred_flag\":" << (pps.weighted_pred_flag ? 1 : 0)
      << ",\"weighted_bipred_idc\":" << pps.weighted_bipred_idc
      << ",\"deblocking_filter_control_present_flag\":"
      << (pps.deblocking_filter_control_present_flag ? 1 : 0) << "}";
}

void write_types(std::ostringstream& out, const PictureTypeCounts& counts) {
  out << "{\"I\":" << counts.i << ",\"P\":" << counts.p
      << ",\"B\":" << counts.b << ",\"SP\":" << counts.sp
      << ",\"SI\":" << counts.si << ",\"total\":" << counts.total() << "}";
}

}  // namespace

std::string format_json_report(const AnalysisReport& report,
                               const std::string& input_name) {
  std::ostringstream out;
  const StreamStats& stats = report.stats;
  out << "{";
  out << "\"tool\":\"h264scan\",\"schema_version\":1,";
  out << "\"input\":";
  write_json_string(out, input_name);
  out << ",\"ok\":" << (report.ok ? "true" : "false")
      << ",\"incomplete\":" << (report.incomplete ? "true" : "false");
  out << ",\"nals\":" << stats.nal_count
      << ",\"sps_count\":" << stats.sps_count
      << ",\"pps_count\":" << stats.pps_count
      << ",\"sei_count\":" << stats.sei_count
      << ",\"leading_zero_bytes\":" << report.leading_zero_bytes
      << ",\"empty_nals\":" << report.empty_nal_count
      << ",\"truncated_start_code\":"
      << (report.had_truncated_start_code ? "true" : "false");
  out << ",\"sps\":[";
  bool first = true;
  for (const auto& entry : report.sets.sps) {
    if (!first) out << ",";
    first = false;
    write_json_sps(out, entry.second);
  }
  out << "],\"pps\":[";
  first = true;
  for (const auto& entry : report.sets.pps) {
    if (!first) out << ",";
    first = false;
    write_json_pps(out, entry.second);
  }
  out << "]";
  out << ",\"pictures\":" << stats.pictures
      << ",\"slices\":" << stats.slices
      << ",\"picture_types\":";
  write_types(out, stats.all_types);
  out << ",\"frame_picture_types\":";
  write_types(out, stats.picture_types);
  out << ",\"field_picture_types\":";
  write_types(out, stats.field_types);
  out << ",\"field_pictures\":" << stats.field_pictures
      << ",\"frame_pictures\":" << stats.frame_pictures;
  out << ",\"gop_count\":" << stats.gop_count
      << ",\"ungrouped_prefix_pictures\":"
      << stats.ungrouped_picture_count;
  out << ",\"gops\":[";
  for (std::size_t i = 0; i < report.gops.gops.size(); ++i) {
    const Gop& gop = report.gops.gops[i];
    if (i != 0) out << ",";
    out << "{\"index\":" << gop.index
        << ",\"start_offset_bytes\":" << gop.start_offset_bytes
        << ",\"picture_count\":" << gop.picture_count
        << ",\"frame_num\":" << gop.frame_num << "}";
  }
  out << "]";
  out << ",\"diagnostics\":[";
  for (std::size_t i = 0; i < report.warnings.size(); ++i) {
    if (i != 0) out << ",";
    write_json_string(out, report.warnings[i]);
  }
  out << "]}";
  return out.str();
}

std::string format_text_report(const AnalysisReport& report,
                               const std::string& input_name) {
  std::ostringstream out;
  const StreamStats& stats = report.stats;
  out << "h264scan report\n";
  out << "input: " << input_name << "\n";
  out << "status: " << (report.ok ? "ok" : "not-ok")
      << (report.incomplete ? " (incomplete)" : "") << "\n";
  out << "nals: " << stats.nal_count
      << " sps=" << stats.sps_count
      << " pps=" << stats.pps_count
      << " sei=" << stats.sei_count << "\n";
  out << "leading_zero_bytes: " << report.leading_zero_bytes
      << " empty_nals: " << report.empty_nal_count
      << " truncated_start_code: "
      << (report.had_truncated_start_code ? "yes" : "no") << "\n";
  for (const auto& entry : report.sets.sps) {
    const Sps& sps = entry.second;
    out << "sps[" << sps.sps_id << "]: profile_idc=" << sps.profile_idc
        << " level_idc=" << sps.level_idc
        << " width=" << sps.width_pixels() << " height=" << sps.height_pixels()
        << " pic_width_in_mbs=" << sps.pic_width_in_mbs
        << " pic_height_in_map_units=" << sps.pic_height_in_map_units
        << " frame_mbs_only_flag=" << (sps.frame_mbs_only_flag ? 1 : 0)
        << " pic_order_cnt_type=" << sps.pic_order_cnt_type
        << " log2_max_frame_num=" << sps.log2_max_frame_num
        << " log2_max_poc_lsb=" << sps.log2_max_pic_order_cnt_lsb
        << "\n";
  }
  for (const auto& entry : report.sets.pps) {
    out << "pps[" << entry.second.pps_id
        << "]: sps_id=" << entry.second.sps_id << "\n";
  }
  out << "pictures: " << stats.pictures
      << " slices: " << stats.slices
      << " fields: " << stats.field_pictures
      << " frames: " << stats.frame_pictures << "\n";
  out << "types: I=" << stats.all_types.i << " P=" << stats.all_types.p
      << " B=" << stats.all_types.b << " (frames I=" << stats.picture_types.i
      << " P=" << stats.picture_types.p << " B=" << stats.picture_types.b
      << "; fields I=" << stats.field_types.i << " P=" << stats.field_types.p
      << " B=" << stats.field_types.b << ")\n";
  out << "gop_count: " << stats.gop_count
      << " ungrouped_prefix_pictures: " << stats.ungrouped_picture_count
      << "\n";
  for (const Gop& gop : report.gops.gops) {
    out << "  gop[" << gop.index << "]: start_offset_bytes="
        << gop.start_offset_bytes << " picture_count=" << gop.picture_count
        << " frame_num=" << gop.frame_num << "\n";
  }
  for (const std::string& warning : report.warnings) {
    out << "warning: " << warning << "\n";
  }
  return out.str();
}

}  // namespace h264
