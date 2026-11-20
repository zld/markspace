#include "h264/fixture_writer.hpp"

#include <algorithm>

#include "h264/bit_writer.hpp"

namespace h264 {

namespace {

constexpr int kSpsId = 0;
constexpr int kPpsId = 0;

std::vector<std::uint8_t> build_sps(const FixtureSpec& spec, int width_pixels,
                                    int height_pixels) {
  BitWriter w;
  w.write_bits(static_cast<std::uint32_t>(spec.profile_idc), 8);
  w.write_bits(0, 8);  // constraint_set flags
  w.write_bits(static_cast<std::uint32_t>(spec.level_idc), 8);
  w.write_ue(kSpsId);
  w.write_ue(static_cast<std::uint32_t>(spec.log2_max_frame_num - 4));
  w.write_ue(0);  // pic_order_cnt_type
  w.write_ue(static_cast<std::uint32_t>(spec.log2_max_pic_order_cnt_lsb - 4));
  w.write_ue(2);  // max_num_ref_frames
  w.write_bit(false);  // gaps_in_frame_num_value_allowed_flag
  w.write_ue(static_cast<std::uint32_t>(spec.pic_width_in_mbs - 1));
  w.write_ue(static_cast<std::uint32_t>(spec.pic_height_in_map_units - 1));
  w.write_bit(spec.frame_mbs_only);
  if (!spec.frame_mbs_only) w.write_bit(false);  // mb_adaptive_frame_field_flag
  w.write_bit(true);   // direct_8x8_inference_flag
  w.write_bit(false);  // frame_cropping_flag
  w.write_bit(false);  // vui_parameters_present_flag
  w.append_trailing_bits();
  (void)width_pixels;
  (void)height_pixels;
  return w.bytes();
}

std::vector<std::uint8_t> build_pps() {
  BitWriter w;
  w.write_ue(kPpsId);
  w.write_ue(kSpsId);
  w.write_bit(false);  // entropy_coding_mode_flag (CAVLC)
  w.write_bit(false);  // bottom_field_pic_order_in_frame_present_flag
  w.write_ue(0);       // num_slice_groups_minus1 (no FMO)
  w.write_ue(0);       // num_ref_idx_l0_default_active_minus1
  w.write_ue(0);       // num_ref_idx_l1_default_active_minus1
  w.write_bit(false);  // weighted_pred_flag
  w.write_bits(0, 2);  // weighted_bipred_idc
  w.write_se(0);       // pic_init_qp_minus26
  w.write_se(0);       // pic_init_qs_minus26
  w.write_se(0);       // chroma_qp_index_offset
  w.write_bit(true);   // deblocking_filter_control_present_flag
  w.write_bit(false);  // constrained_intra_pred_flag
  w.write_bit(false);  // redundant_pic_cnt_present_flag
  w.append_trailing_bits();
  return w.bytes();
}

struct SliceSpec {
  int slice_type;  // raw slice_type code (7 = all-I, 5 = all-P, 6 = all-B)
  int frame_num;
  bool field;
  bool bottom_field;
  int poc_lsb;
  int nal_ref_idc;
  bool idr;
  int first_mb;
  bool second_slice_of_picture;
};

std::vector<std::uint8_t> build_slice(const SliceSpec& s, int log2_frame_num,
                                      int log2_poc_lsb, int sps_id) {
  BitWriter w;
  w.write_ue(static_cast<std::uint32_t>(s.first_mb));
  w.write_ue(static_cast<std::uint32_t>(s.slice_type));
  w.write_ue(kPpsId);
  w.write_bits(static_cast<std::uint32_t>(s.frame_num), log2_frame_num);
  // The fixture is interlaced, so field_pic_flag is always present.
  w.write_bit(s.field);
  if (s.field) w.write_bit(s.bottom_field);
  if (s.idr) w.write_ue(static_cast<std::uint32_t>(s.field ? 3 : 0));
  w.write_bits(static_cast<std::uint32_t>(s.poc_lsb), log2_poc_lsb);
  const int base_type = s.slice_type % 5;
  if (base_type == 1) w.write_bit(true);  // direct_spatial_mv_pred_flag
  if (base_type == 0 || base_type == 1) {
    w.write_bit(false);  // num_ref_idx_active_override_flag
    w.write_bit(false);  // ref_pic_list_modification_flag_l0
    if (base_type == 1) w.write_bit(false);  // ref_pic_list_modification_l1
  }
  if (s.nal_ref_idc != 0) {
    if (s.idr) {
      w.write_bit(false);  // no_output_of_prior_pics_flag
      w.write_bit(false);  // long_term_reference_flag
    } else {
      w.write_bit(false);  // adaptive_ref_pic_marking_mode_flag
    }
  }
  // Coded slice data is not needed for header analysis; terminate the RBSP.
  w.append_trailing_bits();
  (void)sps_id;
  (void)s.second_slice_of_picture;
  return w.bytes();
}

}  // namespace

FixtureSpec fixture_spec(FixtureProfile profile) {
  FixtureSpec spec;
  spec.profile_idc = 66;
  spec.level_idc = 30;
  spec.pic_width_in_mbs = 4;
  spec.pic_height_in_map_units = 2;
  spec.frame_mbs_only = profile != FixtureProfile::kInterlacedMultislice;
  spec.log2_max_frame_num = 4;
  spec.log2_max_pic_order_cnt_lsb = 6;
  return spec;
}

std::string fixture_profile_name(FixtureProfile profile) {
  switch (profile) {
    case FixtureProfile::kBaseline: return "baseline";
    case FixtureProfile::kInterlacedMultislice: return "interlaced-multislice";
  }
  return "unknown";
}

bool parse_fixture_profile(const std::string& name, FixtureProfile* out) {
  if (name == "baseline" || name == "progressive-baseline") {
    *out = FixtureProfile::kBaseline;
    return true;
  }
  if (name == "interlaced-multislice" || name == "interlaced") {
    *out = FixtureProfile::kInterlacedMultislice;
    return true;
  }
  return false;
}

void append_nal(std::vector<std::uint8_t>* out, int nal_ref_idc,
                int nal_unit_type, const std::vector<std::uint8_t>& rbsp,
                bool four_byte_start_code) {
  if (four_byte_start_code) out->push_back(0x00);
  out->push_back(0x00);
  out->push_back(0x00);
  out->push_back(0x01);
  out->push_back(static_cast<std::uint8_t>(((nal_ref_idc & 3) << 5) |
                                           (nal_unit_type & 0x1F)));
  const std::vector<std::uint8_t> escaped = rbsp_escape(rbsp);
  out->insert(out->end(), escaped.begin(), escaped.end());
}

std::vector<std::uint8_t> build_fixture(const FixtureOptions& options) {
  const FixtureSpec spec = fixture_spec(options.profile);
  const bool four = options.use_four_byte_start_codes;
  std::vector<std::uint8_t> out;
  out.assign(options.leading_zero_bytes, 0);

  append_nal(&out, 3, 7, build_sps(spec, 0, 0), four);
  append_nal(&out, 3, 8, build_pps(), four);

  if (options.profile == FixtureProfile::kBaseline) {
    // IDR picture (two slices), then one P picture: 2 pictures, 3 slices.
    SliceSpec idr;
    idr.slice_type = 7;
    idr.frame_num = 0;
    idr.field = false;
    idr.bottom_field = false;
    idr.poc_lsb = 0;
    idr.nal_ref_idc = 3;
    idr.idr = true;
    idr.first_mb = 0;
    idr.second_slice_of_picture = false;
    append_nal(&out, 3, 5,
               build_slice(idr, spec.log2_max_frame_num,
                           spec.log2_max_pic_order_cnt_lsb, kSpsId),
               four);
    idr.first_mb = 8;
    idr.second_slice_of_picture = true;
    append_nal(&out, 3, 5,
               build_slice(idr, spec.log2_max_frame_num,
                           spec.log2_max_pic_order_cnt_lsb, kSpsId),
               four);
    SliceSpec p;
    p.slice_type = 5;
    p.frame_num = 1;
    p.field = false;
    p.bottom_field = false;
    p.poc_lsb = 2;
    p.nal_ref_idc = 2;
    p.idr = false;
    p.first_mb = 0;
    p.second_slice_of_picture = false;
    append_nal(&out, 2, 1,
               build_slice(p, spec.log2_max_frame_num,
                           spec.log2_max_pic_order_cnt_lsb, kSpsId),
               four);
    return out;
  }

  // Demo interlaced stream: 5 pictures, 8 slices.
  //   IDR top field (1 slice), IDR bottom field (1 slice),
  //   P frame (2 slices), B frame (2 slices), B frame (2 slices).
  struct PicturePlan {
    SliceSpec base;
    int slice_count;
    bool idr;
  };
  const PicturePlan plan[] = {
      {{7, 0, true, false, 0, 3, true, 0, false}, 1, true},
      {{7, 0, true, true, 0, 3, true, 0, false}, 1, true},
      {{5, 1, false, false, 2, 2, false, 0, false}, 2, false},
      {{6, 2, false, false, 1, 0, false, 0, false}, 2, false},
      {{6, 3, false, false, 3, 0, false, 0, false}, 2, false},
  };
  const int start_mb[2] = {0, 4};
  for (const PicturePlan& picture : plan) {
    for (int i = 0; i < picture.slice_count; ++i) {
      SliceSpec slice = picture.base;
      slice.first_mb = start_mb[i];
      slice.second_slice_of_picture = i > 0;
      append_nal(&out, slice.nal_ref_idc, slice.idr ? 5 : 1,
                 build_slice(slice, spec.log2_max_frame_num,
                             spec.log2_max_pic_order_cnt_lsb, kSpsId),
                 four);
    }
  }
  return out;
}

}  // namespace h264
