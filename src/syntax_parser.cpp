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



Pps parse_pps(const std::uint8_t* payload, std::size_t size,
              std::size_t nal_offset, const ParameterSets& sets) {
  const std::vector<std::uint8_t> rbsp =
      rbsp_unescape(payload, size, nal_offset);
  BitReader r(rbsp.data(), rbsp.size(), nal_offset);

  Pps pps;
  pps.nal_offset = nal_offset;
  pps.pps_id = static_cast<int>(r.read_ue());
  if (pps.pps_id > 255) fail("PPS id out of range", nal_offset);
  pps.sps_id = static_cast<int>(r.read_ue());
  if (pps.sps_id > 31) fail("PPS references an out-of-range SPS id", nal_offset);

  pps.entropy_coding_mode_flag = r.read_bit();
  pps.bottom_field_pic_order_in_frame_present_flag = r.read_bit();
  if (r.read_ue() != 0) {
    fail("FMO (slice groups) is not supported", nal_offset, 0, true);
  }
  pps.num_ref_idx_l0_default_active = static_cast<int>(r.read_ue()) + 1;
  pps.num_ref_idx_l1_default_active = static_cast<int>(r.read_ue()) + 1;
  pps.weighted_pred_flag = r.read_bit();
  pps.weighted_bipred_idc = static_cast<int>(r.read_bits(2));
  if (pps.weighted_bipred_idc == 3) {
    fail("weighted_bipred_idc == 3 is reserved", nal_offset);
  }
  pps.pic_init_qp_minus26 = r.read_se();
  pps.pic_init_qs_minus26 = r.read_se();
  pps.chroma_qp_index_offset = r.read_se();
  pps.deblocking_filter_control_present_flag = r.read_bit();
  pps.constrained_intra_pred_flag = r.read_bit();
  pps.redundant_pic_cnt_present_flag = r.read_bit();

  if (r.more_rbsp_data()) {
    pps.transform_8x8_mode_flag = r.read_bit();
    if (r.read_bit()) {  // pic_scaling_matrix_present_flag
      const Sps& sps = sets.require_sps(pps.sps_id, nal_offset);
      const int lists = 6 + ((sps.chroma_format_idc != 3) ? 2 : 6) *
                                (pps.transform_8x8_mode_flag ? 1 : 0);
      for (int i = 0; i < lists; ++i) {
        if (r.read_bit()) skip_scaling_list(r, i < 6 ? 16 : 64);
      }
    }
    pps.second_chroma_qp_index_offset = r.read_se();
  }

  if (pps.pic_init_qp_minus26 < -26 || pps.pic_init_qp_minus26 > 25) {
    fail("pic_init_qp_minus26 out of range", nal_offset);
  }
  if (sets.sps.find(pps.sps_id) == sets.sps.end()) {
    fail("PPS references unknown SPS id " + std::to_string(pps.sps_id),
         nal_offset, 0, true);
  }
  return pps;
}


namespace {

void skip_ref_pic_list_modification(BitReader& r, SliceKind kind,
                                    std::size_t nal_offset) {
  const int lists = kind == SliceKind::kB ? 2 : 1;
  for (int list = 0; list < lists; ++list) {
    if (!r.read_bit()) continue;  // ref_pic_list_modification_flag_lX
    for (int guard = 0;; ++guard) {
      if (guard > 65) {
        fail("ref_pic_list_modification loop too long", nal_offset);
      }
      const std::uint32_t idc = r.read_ue();
      if (idc == 3) break;  // modification_of_pic_nums_idc == 3
      if (idc > 3) fail("invalid ref_pic_list_modification idc", nal_offset);
      r.read_ue();
    }
  }
}

}  // namespace

namespace {

void skip_pred_weight_table(BitReader& r, SliceKind kind, const Sps& sps,
                            const Pps& pps) {
  const bool is_b = kind == SliceKind::kB;
  r.read_ue();  // luma_log2_weight_denom
  if (sps.chroma_array_type() != 0) r.read_ue();  // chroma_log2_weight_denom
  const int lists = is_b ? 2 : 1;
  const int counts[2] = {pps.num_ref_idx_l0_default_active,
                         pps.num_ref_idx_l1_default_active};
  for (int list = 0; list < lists; ++list) {
    for (int i = 0; i < counts[list]; ++i) {
      if (r.read_bit()) {
        r.read_se();
        r.read_se();
      }
      if (sps.chroma_array_type() != 0 && r.read_bit()) {
        for (int c = 0; c < 4; ++c) r.read_se();
      }
    }
  }
}

void skip_dec_ref_pic_marking(BitReader& r, bool idr, std::size_t nal_offset) {
  if (idr) {
    r.read_bit();  // no_output_of_prior_pics_flag
    r.read_bit();  // long_term_reference_flag
    return;
  }
  if (!r.read_bit()) return;  // adaptive_ref_pic_marking_mode_flag
  for (int guard = 0; guard <= 65; ++guard) {
    const std::uint32_t op = r.read_ue();
    if (op == 0) return;
    if (op > 6) {
      fail("invalid memory_management_control_operation", nal_offset);
    }
    r.read_ue();  // difference_of_pic_nums_minus1 / long_term_pic_num /
                  // max_long_term_frame_idx_plus1
    if (op == 3 || op == 6) r.read_ue();  // long_term_frame_idx
    if (guard == 65) fail("dec_ref_pic_marking loop too long", nal_offset);
  }
}

}  // namespace

SliceHeader parse_slice_header(const std::uint8_t* payload, std::size_t size,
                               int nal_unit_type, int nal_ref_idc,
                               std::size_t nal_offset,
                               const ParameterSets& sets) {
  const std::vector<std::uint8_t> rbsp =
      rbsp_unescape(payload, size, nal_offset);
  BitReader r(rbsp.data(), rbsp.size(), nal_offset);

  SliceHeader h;
  h.nal_unit_type = nal_unit_type;
  h.idr = nal_unit_type == 5;
  h.nal_ref_idc = nal_ref_idc;
  h.nal_offset = nal_offset;

  h.first_mb_in_slice = static_cast<int>(r.read_ue());
  const std::uint32_t kind = r.read_ue();
  if (kind > 9) fail("slice_type out of range", nal_offset);
  h.slice_kind = static_cast<SliceKind>(kind % 5);
  h.pps_id = static_cast<int>(r.read_ue());
  if (h.pps_id > 255) fail("slice references out-of-range PPS id", nal_offset);

  const Pps& pps = sets.require_pps(h.pps_id, nal_offset);
  const Sps& sps = sets.require_sps(pps.sps_id, nal_offset);
  h.sps_id = pps.sps_id;

  if (sps.chroma_array_type() == 0) {
    h.colour_plane_id = static_cast<int>(r.read_bits(2));
    h.separate_colour_plane = true;
  }

  h.frame_num = static_cast<int>(
      r.read_bits(static_cast<int>(sps.log2_max_frame_num)));
  if (!sps.frame_mbs_only_flag) {
    h.field_pic_flag = r.read_bit();
    if (h.field_pic_flag) h.bottom_field_flag = r.read_bit();
  }
  if (h.idr) h.idr_pic_id = static_cast<int>(r.read_ue());

  if (sps.pic_order_cnt_type == 0) {
    h.pic_order_cnt_lsb = static_cast<int>(
        r.read_bits(static_cast<int>(sps.log2_max_pic_order_cnt_lsb)));
    if (pps.bottom_field_pic_order_in_frame_present_flag && !h.field_pic_flag) {
      h.delta_pic_order_cnt_bottom = r.read_se();
    }
  } else if (sps.pic_order_cnt_type == 1 &&
             !sps.delta_pic_order_always_zero_flag) {
    h.delta_pic_order_cnt0 = r.read_se();
    if (pps.bottom_field_pic_order_in_frame_present_flag && !h.field_pic_flag) {
      h.delta_pic_order_cnt1 = r.read_se();
    }
  }
  if (pps.redundant_pic_cnt_present_flag) {
    h.redundant_pic_cnt = static_cast<int>(r.read_ue());
  }

  const bool is_b = h.slice_kind == SliceKind::kB;
  const bool is_p_sp = h.slice_kind == SliceKind::kP ||
                       h.slice_kind == SliceKind::kSP;
  if (is_b) r.read_bit();  // direct_spatial_mv_pred_flag

  if (is_p_sp || is_b) {
    if (r.read_bit()) {  // num_ref_idx_active_override_flag
      r.read_ue();       // num_ref_idx_l0_active_minus1
      if (is_b) r.read_ue();
    }
  }

  const bool is_i = h.slice_kind == SliceKind::kI ||
                    h.slice_kind == SliceKind::kSI;
  if (!is_i) skip_ref_pic_list_modification(r, h.slice_kind, nal_offset);
  if ((pps.weighted_pred_flag && is_p_sp) ||
      (pps.weighted_bipred_idc == 1 && is_b)) {
    skip_pred_weight_table(r, h.slice_kind, sps, pps);
  }
  if (nal_ref_idc != 0) skip_dec_ref_pic_marking(r, h.idr, nal_offset);

  return h;
}

}  // namespace h264
