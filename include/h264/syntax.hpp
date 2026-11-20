#pragma once

#include <cstddef>
#include <cstdint>
#include <map>
#include <string>
#include <vector>

#include "h264/nal.hpp"

namespace h264 {

enum class SliceKind { kP = 0, kB = 1, kI = 2, kSP = 3, kSI = 4 };

const char* slice_kind_name(SliceKind kind);

struct Sps {
  int sps_id = 0;
  int profile_idc = 0;
  std::uint8_t constraint_flags = 0;
  int level_idc = 0;
  int chroma_format_idc = 1;
  bool separate_colour_plane_flag = false;
  int bit_depth_luma_minus8 = 0;
  int bit_depth_chroma_minus8 = 0;
  int log2_max_frame_num = 4;
  int pic_order_cnt_type = 0;
  int log2_max_pic_order_cnt_lsb = 4;
  bool delta_pic_order_always_zero_flag = false;
  int offset_for_non_ref_pic = 0;
  int offset_for_top_to_bottom_field = 0;
  std::vector<int> offset_for_ref_frame;
  int max_num_ref_frames = 0;
  bool gaps_in_frame_num_value_allowed_flag = false;
  int pic_width_in_mbs = 0;
  int pic_height_in_map_units = 0;
  bool frame_mbs_only_flag = true;
  bool mb_adaptive_frame_field_flag = false;
  bool direct_8x8_inference_flag = false;
  bool frame_cropping_flag = false;
  int frame_crop_left_offset = 0;
  int frame_crop_right_offset = 0;
  int frame_crop_top_offset = 0;
  int frame_crop_bottom_offset = 0;
  bool vui_parameters_present_flag = false;

  std::size_t nal_offset = 0;
  int chroma_array_type() const;
  int width_pixels() const;
  int height_pixels() const;
};

struct Pps {
  int pps_id = 0;
  int sps_id = 0;
  bool entropy_coding_mode_flag = false;
  bool bottom_field_pic_order_in_frame_present_flag = false;
  int num_ref_idx_l0_default_active = 1;
  int num_ref_idx_l1_default_active = 1;
  bool weighted_pred_flag = false;
  int weighted_bipred_idc = 0;
  int pic_init_qp_minus26 = 0;
  int pic_init_qs_minus26 = 0;
  int chroma_qp_index_offset = 0;
  bool deblocking_filter_control_present_flag = false;
  bool constrained_intra_pred_flag = false;
  bool redundant_pic_cnt_present_flag = false;
  bool transform_8x8_mode_flag = false;
  int second_chroma_qp_index_offset = 0;
  std::size_t nal_offset = 0;
};

struct ParameterSets {
  std::map<int, Sps> sps;
  std::map<int, Pps> pps;

  const Sps& require_sps(int id, std::size_t nal_offset) const;
  const Pps& require_pps(int id, std::size_t nal_offset) const;
};

struct SliceHeader {
  int nal_unit_type = 1;
  bool idr = false;
  int nal_ref_idc = 0;
  std::size_t nal_offset = 0;

  int first_mb_in_slice = 0;
  SliceKind slice_kind = SliceKind::kP;
  int pps_id = 0;
  int sps_id = 0;
  int colour_plane_id = 0;
  bool separate_colour_plane = false;
  int frame_num = 0;
  bool field_pic_flag = false;
  bool bottom_field_flag = false;
  int idr_pic_id = 0;
  int pic_order_cnt_lsb = 0;
  int delta_pic_order_cnt_bottom = 0;
  int delta_pic_order_cnt0 = 0;
  int delta_pic_order_cnt1 = 0;
  int redundant_pic_cnt = 0;
};

Sps parse_sps(const std::uint8_t* payload, std::size_t size,
              std::size_t nal_offset);
Pps parse_pps(const std::uint8_t* payload, std::size_t size,
              std::size_t nal_offset, const ParameterSets& sets);
SliceHeader parse_slice_header(const std::uint8_t* payload, std::size_t size,
                               int nal_unit_type, int nal_ref_idc,
                               std::size_t nal_offset,
                               const ParameterSets& sets);

}  // namespace h264
