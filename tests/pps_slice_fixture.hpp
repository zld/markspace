// PPS and slice-header fixture synthesis for tests and sample generation.
#pragma once

#include <cstdint>
#include <vector>

#include "annexb_fixture.hpp"
#include "bit_writer.hpp"
#include "sps_fixture.hpp"

namespace selftest {

struct PpsFixture {
    std::uint32_t id = 0;
    std::uint32_t sps_id = 0;
    bool entropy_coding_mode = false;
    bool bottom_field_pic_order_in_frame_present = false;
    std::uint32_t num_ref_idx_l0_default_active_minus1 = 0;
    std::uint32_t num_ref_idx_l1_default_active_minus1 = 0;
    bool weighted_pred = false;
    std::uint32_t weighted_bipred_idc = 0;
    std::int32_t pic_init_qp_minus26 = 0;
    std::int32_t pic_init_qs_minus26 = 0;
    std::int32_t chroma_qp_index_offset = 0;
    bool deblocking_filter_control_present = true;
    bool constrained_intra_pred = false;
    bool redundant_pic_cnt_present = false;
};

inline std::vector<std::uint8_t> build_pps_rbsp(const PpsFixture& f) {
    BitWriter w;
    w.ue(f.id);
    w.ue(f.sps_id);
    w.bit(f.entropy_coding_mode);
    w.bit(f.bottom_field_pic_order_in_frame_present);
    w.ue(0);  // num_slice_groups_minus1
    w.ue(f.num_ref_idx_l0_default_active_minus1);
    w.ue(f.num_ref_idx_l1_default_active_minus1);
    w.bit(f.weighted_pred);
    w.bits(f.weighted_bipred_idc, 2);
    w.se(f.pic_init_qp_minus26);
    w.se(f.pic_init_qs_minus26);
    w.se(f.chroma_qp_index_offset);
    w.bit(f.deblocking_filter_control_present);
    w.bit(f.constrained_intra_pred);
    w.bit(f.redundant_pic_cnt_present);
    w.rbsp_trailing_bits();
    return w.bytes();
}

inline std::vector<std::uint8_t> build_pps_nal(const PpsFixture& f,
                                               bool four_byte = true) {
    return annexb_nal(0x68, escape_rbsp(build_pps_rbsp(f)), four_byte);
}

struct SliceFixture {
    std::uint32_t first_mb_in_slice = 0;
    std::uint32_t slice_type = 2;  // I
    std::uint32_t pps_id = 0;
    std::uint32_t colour_plane_id = 0;
    std::uint32_t frame_num = 0;
    bool field_pic_flag = false;
    bool bottom_field_flag = false;
    std::uint32_t idr_pic_id = 0;
    std::uint32_t pic_order_cnt_lsb = 0;
    std::int32_t delta_pic_order_cnt_bottom = 0;
    int nal_ref_idc = 3;
    bool is_idr = true;
    // Extra bytes after a byte-aligned header stand in for slice data.
    bool include_slice_data = true;
    std::vector<std::uint8_t> slice_data = {0x80};
};

// Builds a slice header RBSP. The SPS and PPS fixtures must be the ones the
// stream actually carries, because both drive the header layout.
template <typename SpsF>
std::vector<std::uint8_t> build_slice_header_rbsp(const SliceFixture& s,
                                                  const SpsF& sps,
                                                  const PpsFixture& pps) {
    const std::uint32_t type = s.slice_type % 5;
    BitWriter w;
    w.ue(s.first_mb_in_slice);
    w.ue(s.slice_type);
    w.ue(s.pps_id);
    if (sps.separate_colour_plane) w.bits(s.colour_plane_id, 2);
    w.bits(s.frame_num, static_cast<int>(sps.log2_max_frame_num_minus4 + 4));
    if (!sps.frame_mbs_only) {
        w.bit(s.field_pic_flag);
        if (s.field_pic_flag) w.bit(s.bottom_field_flag);
    }
    if (s.is_idr) w.ue(s.idr_pic_id);
    w.bits(s.pic_order_cnt_lsb,
           static_cast<int>(sps.log2_max_pic_order_cnt_lsb_minus4 + 4));
    if (pps.bottom_field_pic_order_in_frame_present && !s.field_pic_flag) {
        w.se(s.delta_pic_order_cnt_bottom);
    }

    if (type == 1) w.bit(false);  // direct_spatial_mv_pred_flag
    if (type != 2 && type != 4) {
        w.bit(false);  // num_ref_idx_active_override_flag
        w.bit(false);  // ref_pic_list_modification_flag_l0
        if (type == 1) w.bit(false);  // ref_pic_list_modification_flag_l1
    }
    if (s.nal_ref_idc != 0) {
        if (s.is_idr) {
            w.bit(false);  // no_output_of_prior_pics_flag
            w.bit(false);  // long_term_reference_flag
        } else {
            w.bit(false);  // adaptive_ref_pic_marking_mode_flag
        }
    }
    w.byte_align_zero();
    std::vector<std::uint8_t> bytes = w.bytes();
    if (s.include_slice_data) {
        // Stand-in slice data keeps the header from looking truncated.
        bytes.push_back(0x00);
        bytes.insert(bytes.end(), s.slice_data.begin(), s.slice_data.end());
    }
    return bytes;
}

}  // namespace selftest
