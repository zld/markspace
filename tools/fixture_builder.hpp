#ifndef H264_FIXTURE_BUILDER_HPP
#define H264_FIXTURE_BUILDER_HPP

#include <cstdint>
#include <vector>

#include "bit_writer.hpp"
#include "nal_parser.hpp"
#include "slice_parser.hpp"

// Deterministic, hand-constructed Annex B stream used by both the fixture
// generator and the unit tests. Nothing here is random: every byte follows
// from fixed syntax values, so regression output is byte-stable.
namespace h264::fixture {

// NAL header byte components (forbidden bit always 0).
inline std::uint8_t nal_header(unsigned nal_ref_idc, unsigned nal_type) {
    return static_cast<std::uint8_t>(((nal_ref_idc & 0x3u) << 5) |
                                     (nal_type & 0x1Fu));
}

// Append one NAL using either 3- or 4-byte start codes. `four_byte` adds the
// Annex B zero_byte that real streams place before SPS/PPS/VCL NALs.
inline void append_nal(std::vector<std::uint8_t>& out,
                       const std::vector<std::uint8_t>& rbsp,
                       unsigned nal_ref_idc, unsigned nal_type,
                       bool four_byte) {
    out.push_back(0x00);
    out.push_back(0x00);
    if (four_byte) out.push_back(0x00);
    out.push_back(0x01);
    std::vector<std::uint8_t> ebsp = escape_ebsp(rbsp);
    out.push_back(nal_header(nal_ref_idc, nal_type));
    out.insert(out.end(), ebsp.begin(), ebsp.end());
}

// Baseline profile SPS: 320x240, frame-only, frame_num 4 bits.
inline std::vector<std::uint8_t> build_sps_baseline() {
    BitWriter w;
    w.write_bits(66, 8);   // profile_idc Baseline
    w.write_bits(0, 8);    // constraint flags
    w.write_bits(30, 8);   // level_idc 3.0
    w.write_ue(0);         // sps_id
    w.write_ue(0);         // log2_max_frame_num_minus4 -> 4 bits
    w.write_ue(0);         // pic_order_cnt_type
    w.write_ue(0);         // log2_max_pic_order_cnt_lsb_minus4
    w.write_ue(1);         // max_num_ref_frames
    w.write_bit(0);        // gaps_in_frame_num_allowed
    w.write_ue(19);        // pic_width_in_mbs_minus1 -> 320
    w.write_ue(14);        // pic_height_in_map_units_minus1 -> 240
    w.write_bit(1);        // frame_mbs_only_flag
    w.write_bit(1);        // direct_8x8_inference_flag
    w.write_bit(0);        // frame_cropping_flag
    w.rbsp_trailing_bits();
    return w.take();
}

// High profile SPS: 640x480, frame-only, 4:2:0 8-bit, with cropping.
inline std::vector<std::uint8_t> build_sps_high() {
    BitWriter w;
    w.write_bits(100, 8);  // High profile
    w.write_bits(0, 8);
    w.write_bits(40, 8);   // level 4.0
    w.write_ue(1);         // sps_id = 1
    w.write_ue(1);         // chroma_format_idc 4:2:0
    w.write_ue(0);         // bit_depth_luma_minus8
    w.write_ue(0);         // bit_depth_chroma_minus8
    w.write_bit(0);        // qpprime_y_zero_transform_bypass
    w.write_bit(0);        // seq_scaling_matrix_present
    w.write_ue(0);         // log2_max_frame_num_minus4
    w.write_ue(0);         // pic_order_cnt_type
    w.write_ue(0);         // log2_max_pic_order_cnt_lsb_minus4
    w.write_ue(2);         // max_num_ref_frames
    w.write_bit(0);        // gaps
    w.write_ue(39);        // 640 / 16 - 1
    w.write_ue(29);        // 480 / 16 - 1
    w.write_bit(1);        // frame_mbs_only_flag
    w.write_bit(1);        // direct_8x8_inference_flag
    w.write_bit(1);        // frame_cropping_flag
    w.write_ue(1); w.write_ue(1);  // crop left/right (4:2:0 -> 2px each)
    w.write_ue(0); w.write_ue(0);  // crop top/bottom
    w.rbsp_trailing_bits();
    return w.take();
}

// Interlaced High SPS: 352x288, field coding allowed, frame_num 6 bits.
inline std::vector<std::uint8_t> build_sps_interlaced() {
    BitWriter w;
    w.write_bits(100, 8);
    w.write_bits(0, 8);
    w.write_bits(30, 8);
    w.write_ue(2);         // sps_id = 2
    w.write_ue(1);         // chroma 4:2:0
    w.write_ue(0); w.write_ue(0);
    w.write_bit(0);
    w.write_bit(0);
    w.write_ue(2);         // log2_max_frame_num_minus4 -> 6-bit frame_num
    w.write_ue(0);
    w.write_ue(0);
    w.write_ue(2);
    w.write_bit(0);
    w.write_ue(21);        // 352 / 16 - 1
    w.write_ue(8);         // 288 / 16 / 2 - 1 (map units, frame_mbs_only=0)
    w.write_bit(0);        // frame_mbs_only_flag = 0 (interlaced)
    w.write_bit(1);        // mb_adaptive_frame_field_flag
    w.write_bit(1);        // direct_8x8
    w.write_bit(0);        // no crop
    w.rbsp_trailing_bits();
    return w.take();
}

inline std::vector<std::uint8_t> build_pps(unsigned pps_id, unsigned sps_id,
                                           bool cabac) {
    BitWriter w;
    w.write_ue(pps_id);
    w.write_ue(sps_id);
    w.write_bit(cabac ? 1 : 0);  // entropy_coding_mode_flag
    w.write_bit(0);              // bottom_field_pic_order_in_frame_present
    w.write_ue(0);               // num_slice_groups_minus1
    w.write_ue(0);               // num_ref_idx_l0_default_active_minus1
    w.write_ue(0);               // num_ref_idx_l1_default_active_minus1
    w.write_bit(0);              // weighted_pred_flag
    w.write_bits(0, 2);          // weighted_bipred_idc
    w.write_se(0);               // pic_init_qp_minus26
    w.write_se(0);               // pic_init_qs_minus26
    w.write_se(0);               // chroma_qp_index_offset
    w.write_bit(1);              // deblocking_filter_control_present
    w.write_bit(0);              // redundant_pic_cnt_present
    w.rbsp_trailing_bits();
    return w.take();
}

// One slice NAL RBSP. `frame_num_bits` must match the active SPS.
inline std::vector<std::uint8_t> build_slice_rbsp(
        unsigned first_mb, unsigned slice_type, unsigned pps_id,
        unsigned frame_num, unsigned frame_num_bits,
        bool field_pic, bool bottom_field, bool idr) {
    BitWriter w;
    w.write_ue(first_mb);
    w.write_ue(slice_type);
    w.write_ue(pps_id);
    w.write_bits(frame_num, frame_num_bits);
    if (field_pic) {
        w.write_bit(1);             // field_pic_flag
        w.write_bit(bottom_field ? 1 : 0);
    }
    if (idr) w.write_ue(0);        // idr_pic_id
    w.rbsp_trailing_bits();
    return w.take();
}

// Empty AUD payload NAL (primary_pic_type plus RBSP trailing bits).
inline std::vector<std::uint8_t> build_aud() {
    BitWriter w;
    w.write_bits(0, 8);  // primary_pic_type = 0 (I slices)
    w.rbsp_trailing_bits();
    return w.take();
}

}  // namespace h264::fixture

#endif
