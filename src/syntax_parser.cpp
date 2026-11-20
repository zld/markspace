#include "h264/syntax.hpp"

#include <cstdio>

namespace h264 {

const char* slice_kind_name(SliceKind kind) {
  switch (kind) {
    case SliceKind::kP: return "P";
    case SliceKind::kB: return "B";
    case SliceKind::kI: return "I";
    case SliceKind::kSP: return "SP";
    case SliceKind::kSI: return "SI";
  }
  return "?";
}

int Sps::chroma_array_type() const {
  return separate_colour_plane_flag ? 0 : chroma_format_idc;
}


int Sps::width_pixels() const {
  const int width = pic_width_in_mbs * 16;
  if (!frame_cropping_flag) return width;
  const int chroma = chroma_array_type();
  const int crop_unit_x = (chroma == 1 || chroma == 2) ? 2 : 1;
  return width -
         crop_unit_x * (frame_crop_left_offset + frame_crop_right_offset);
}

int Sps::height_pixels() const {
  const int map_units = 2 - (frame_mbs_only_flag ? 1 : 0);
  int height = pic_height_in_map_units * map_units * 16;
  if (!frame_cropping_flag) return height;
  const int chroma = chroma_array_type();
  const int crop_unit_y =
      (chroma == 1 || chroma == 2) ? 2 * map_units : map_units;
  return height -
         crop_unit_y * (frame_crop_top_offset + frame_crop_bottom_offset);
}

const Sps& ParameterSets::require_sps(int id, std::size_t nal_offset) const {
  const auto it = sps.find(id);
  if (it == sps.end()) {
    fail("reference to unknown SPS id " + std::to_string(id), nal_offset, 0,
         true);
  }
  return it->second;
}

const Pps& ParameterSets::require_pps(int id, std::size_t nal_offset) const {
  const auto it = pps.find(id);
  if (it == pps.end()) {
    fail("reference to unknown PPS id " + std::to_string(id), nal_offset, 0,
         true);
  }
  return it->second;
}

namespace {

void check_dimension(std::uint64_t value, const char* what,
                     std::size_t nal_offset) {
  if (value == 0 || value > 65536) {
    fail(std::string("invalid ") + what + " in SPS", nal_offset, 0, true);
  }
}

void skip_scaling_list(BitReader& r, int size) {
  int last_scale = 8;
  int next_scale = 8;
  for (int j = 0; j < size; ++j) {
    if (next_scale != 0) {
      const int delta_scale = r.read_se();
      next_scale = (last_scale + delta_scale + 256) % 256;
    }
    if (next_scale != 0) last_scale = next_scale;
  }
}

void read_hrd(BitReader& r, std::size_t nal_offset) {
  const std::uint32_t cpb_cnt = r.read_ue();
  if (cpb_cnt > 31) fail("VUI cpb_cnt_minus1 out of range", nal_offset);
  for (std::uint32_t i = 0; i < cpb_cnt + 1; ++i) {
    r.read_ue();  // bit_rate_value_minus1
    r.read_ue();  // cpb_size_value_minus1
    r.read_bit(); // cbr_flag
  }
  r.read_bits(5);  // initial_cpb_removal_delay_length_minus1
  r.read_bits(5);  // cpb_removal_delay_length_minus1
  r.read_bits(5);  // dpb_output_delay_length_minus1
  r.read_bits(5);  // time_offset_length
}

void skip_vui(BitReader& r, std::size_t nal_offset) {
  if (r.read_bit()) {  // aspect_ratio_info_present_flag
    if (r.read_bits(8) == 255) {  // aspect_ratio_idc == Extended_SAR
      r.read_bits(16);
      r.read_bits(16);
    }
  }
  if (r.read_bit()) r.read_bit();  // overscan_info_present_flag
  if (r.read_bit()) {              // video_signal_type_present_flag
    r.read_bits(3);
    r.read_bit();
    if (r.read_bit()) {
      r.read_bits(8);
      r.read_bits(8);
      r.read_bits(8);
    }
  }
  if (r.read_bit()) {  // chroma_loc_info_present_flag
    r.read_ue();
    r.read_ue();
  }
  if (r.read_bit()) {  // timing_info_present_flag
    const std::uint32_t num_units_in_tick = r.read_bits(32);
    const std::uint32_t time_scale = r.read_bits(32);
    if (num_units_in_tick == 0 || time_scale == 0) {
      fail("VUI timing info has a zero denominator", nal_offset);
    }
    r.read_bit();  // fixed_frame_rate_flag
  }
  const bool nal_hrd = r.read_bit();
  if (nal_hrd) read_hrd(r, nal_offset);
  const bool vcl_hrd = r.read_bit();
  if (vcl_hrd) read_hrd(r, nal_offset);
  if (nal_hrd || vcl_hrd) r.read_bit();  // low_delay_hrd_flag
  r.read_bit();                          // pic_struct_present_flag
  if (r.read_bit()) {                    // bitstream_restriction_flag
    r.read_bit();
    for (int i = 0; i < 6; ++i) r.read_ue();
  }
}

}  // namespace

Sps parse_sps(const std::uint8_t* payload, std::size_t size,
              std::size_t nal_offset) {
  const std::vector<std::uint8_t> rbsp =
      rbsp_unescape(payload, size, nal_offset);
  BitReader r(rbsp.data(), rbsp.size(), nal_offset);

  Sps sps;
  sps.nal_offset = nal_offset;
  sps.profile_idc = static_cast<int>(r.read_bits(8));
  sps.constraint_flags = static_cast<std::uint8_t>(r.read_bits(8));
  sps.level_idc = static_cast<int>(r.read_bits(8));
  sps.sps_id = static_cast<int>(r.read_ue());
  if (sps.sps_id > 31) fail("SPS id out of range", nal_offset);

  switch (sps.profile_idc) {
    case 100: case 110: case 122: case 244: case 44:
    case 83: case 86: case 118: case 128: case 138:
    case 139: case 134: case 135: {
      sps.chroma_format_idc = static_cast<int>(r.read_ue());
      if (sps.chroma_format_idc > 3) {
        fail("chroma_format_idc out of range", nal_offset);
      }
      if (sps.chroma_format_idc == 3) {
        sps.separate_colour_plane_flag = r.read_bit();
      }
      sps.bit_depth_luma_minus8 = static_cast<int>(r.read_ue());
      sps.bit_depth_chroma_minus8 = static_cast<int>(r.read_ue());
      if (sps.bit_depth_luma_minus8 > 8 || sps.bit_depth_chroma_minus8 > 8) {
        fail("bit depth out of supported range (max 16 bit)", nal_offset);
      }
      r.read_bit();         // qpprime_y_zero_transform_bypass_flag
      if (r.read_bit()) {   // seq_scaling_matrix_present_flag
        const int lists = sps.chroma_format_idc != 3 ? 8 : 12;
        for (int i = 0; i < lists; ++i) {
          if (r.read_bit()) skip_scaling_list(r, i < 6 ? 16 : 64);
        }
      }
      break;
    }
    default:
      break;
  }

  const int log2_max_frame_num_minus4 = static_cast<int>(r.read_ue());
  if (log2_max_frame_num_minus4 > 12) {
    fail("log2_max_frame_num_minus4 out of range", nal_offset);
  }
  sps.log2_max_frame_num = log2_max_frame_num_minus4 + 4;

  sps.pic_order_cnt_type = static_cast<int>(r.read_ue());
  if (sps.pic_order_cnt_type > 2) {
    fail("pic_order_cnt_type out of range", nal_offset);
  }
  if (sps.pic_order_cnt_type == 0) {
    const int log2_max_poc_lsb_minus4 = static_cast<int>(r.read_ue());
    if (log2_max_poc_lsb_minus4 > 12) {
      fail("log2_max_pic_order_cnt_lsb_minus4 out of range", nal_offset);
    }
    sps.log2_max_pic_order_cnt_lsb = log2_max_poc_lsb_minus4 + 4;
  } else if (sps.pic_order_cnt_type == 1) {
    sps.delta_pic_order_always_zero_flag = r.read_bit();
    sps.offset_for_non_ref_pic = r.read_se();
    sps.offset_for_top_to_bottom_field = r.read_se();
    const std::uint32_t cycle = r.read_ue();
    if (cycle > 255) {
      fail("num_ref_frames_in_pic_order_cnt_cycle too large", nal_offset);
    }
    sps.offset_for_ref_frame.reserve(cycle);
    for (std::uint32_t i = 0; i < cycle; ++i) {
      sps.offset_for_ref_frame.push_back(r.read_se());
    }
  }

  sps.max_num_ref_frames = static_cast<int>(r.read_ue());
  sps.gaps_in_frame_num_value_allowed_flag = r.read_bit();
  const std::uint32_t width_mbs = r.read_ue();
  const std::uint32_t height_map_units = r.read_ue();
  check_dimension(static_cast<std::uint64_t>(width_mbs) + 1,
                  "pic_width_in_mbs_minus1", nal_offset);
  check_dimension(static_cast<std::uint64_t>(height_map_units) + 1,
                  "pic_height_in_map_units_minus1", nal_offset);
  sps.pic_width_in_mbs = static_cast<int>(width_mbs) + 1;
  sps.pic_height_in_map_units = static_cast<int>(height_map_units) + 1;
  sps.frame_mbs_only_flag = r.read_bit();
  if (!sps.frame_mbs_only_flag) {
    sps.mb_adaptive_frame_field_flag = r.read_bit();
  }
  sps.direct_8x8_inference_flag = r.read_bit();
  sps.frame_cropping_flag = r.read_bit();
  if (sps.frame_cropping_flag) {
    sps.frame_crop_left_offset = static_cast<int>(r.read_ue());
    sps.frame_crop_right_offset = static_cast<int>(r.read_ue());
    sps.frame_crop_top_offset = static_cast<int>(r.read_ue());
    sps.frame_crop_bottom_offset = static_cast<int>(r.read_ue());
  }
  sps.vui_parameters_present_flag = r.read_bit();
  if (sps.vui_parameters_present_flag) skip_vui(r, nal_offset);

  if (sps.width_pixels() <= 0 || sps.height_pixels() <= 0) {
    fail("SPS cropping produces a non-positive picture size", nal_offset);
  }
  return sps;
}


}  // namespace h264
