#include "sample_syntax.hpp"

#include <vector>

#include "bit_writer.hpp"

namespace h264inspect {
namespace samplegen {

namespace {

// Inserts 0x03 where three consecutive payload bytes would form a start code.
std::vector<std::uint8_t> escape(const std::vector<std::uint8_t>& rbsp) {
    std::vector<std::uint8_t> out;
    out.reserve(rbsp.size() + 8);
    int zeros = 0;
    for (std::uint8_t byte : rbsp) {
        if (zeros >= 2 && byte <= 0x03) {
            out.push_back(0x03);
            zeros = 0;
        }
        out.push_back(byte);
        zeros = byte == 0x00 ? zeros + 1 : 0;
    }
    return out;
}

}  // namespace

void append_nal(std::vector<std::uint8_t>& out, std::uint8_t header,
                const std::vector<std::uint8_t>& rbsp) {
    out.push_back(0x00);
    out.push_back(0x00);
    out.push_back(0x00);
    out.push_back(0x01);
    out.push_back(header);
    const std::vector<std::uint8_t> payload = escape(rbsp);
    out.insert(out.end(), payload.begin(), payload.end());
}

std::vector<std::uint8_t> build_sps(bool interlaced) {
    BitWriter w;
    w.bits(kProfileIdc, 8);
    w.bits(0, 8);  // constraint flags
    w.bits(kLevelIdc, 8);
    w.ue(0);   // seq_parameter_set_id
    w.ue(1);   // chroma_format_idc: 4:2:0
    w.ue(0);   // bit_depth_luma_minus8
    w.ue(0);   // bit_depth_chroma_minus8
    w.bit(false);  // qpprime_y_zero_transform_bypass_flag
    w.bit(false);  // seq_scaling_matrix_present_flag
    w.ue(kLog2MaxFrameNumMinus4);
    w.ue(0);  // pic_order_cnt_type
    w.ue(kLog2MaxPocLsbMinus4);
    w.ue(2);  // max_num_ref_frames
    w.bit(false);  // gaps_in_frame_num_value_allowed_flag
    w.ue(kPicWidthInMbsMinus1);
    w.ue(kPicHeightInMapUnitsMinus1);
    w.bit(!interlaced);       // frame_mbs_only_flag
    if (interlaced) w.bit(false);  // mb_adaptive_frame_field_flag
    w.bit(true);   // direct_8x8_inference_flag
    w.bit(false);  // frame_cropping_flag
    w.bit(false);  // vui_parameters_present_flag
    w.rbsp_trailing_bits();
    return w.bytes();
}

std::vector<std::uint8_t> build_pps() {
    BitWriter w;
    w.ue(0);   // pic_parameter_set_id
    w.ue(0);   // seq_parameter_set_id
    w.bit(false);  // entropy_coding_mode_flag (CAVLC)
    w.bit(false);  // bottom_field_pic_order_in_frame_present_flag
    w.ue(0);   // num_slice_groups_minus1
    w.ue(0);   // num_ref_idx_l0_default_active_minus1
    w.ue(0);   // num_ref_idx_l1_default_active_minus1
    w.bit(false);  // weighted_pred_flag
    w.bits(0, 2);  // weighted_bipred_idc
    w.se(0);   // pic_init_qp_minus26
    w.se(0);   // pic_init_qs_minus26
    w.se(0);   // chroma_qp_index_offset
    w.bit(true);   // deblocking_filter_control_present_flag
    w.bit(false);  // constrained_intra_pred_flag
    w.bit(false);  // redundant_pic_cnt_present_flag
    w.rbsp_trailing_bits();
    return w.bytes();
}

std::vector<std::uint8_t> build_slice(const SliceSpec& s) {
    BitWriter w;
    w.ue(s.first_mb);
    w.ue(s.slice_type);
    w.ue(0);  // pic_parameter_set_id
    w.bits(s.frame_num, kLog2MaxFrameNumMinus4 + 4);
    if (s.field_pic) {
        w.bit(true);  // field_pic_flag
        w.bit(s.bottom_field);
    }
    if (s.idr) w.ue(0);  // idr_pic_id
    w.bits(s.poc_lsb, kLog2MaxPocLsbMinus4 + 4);
    if (s.slice_type % 5 == 1) w.bit(false);  // direct_spatial_mv_pred_flag
    if (s.slice_type % 5 != 2 && s.slice_type % 5 != 4) {
        w.bit(false);  // num_ref_idx_active_override_flag
        w.bit(false);  // ref_pic_list_modification_flag_l0
        if (s.slice_type % 5 == 1) w.bit(false);  // ref_pic_list_modification_flag_l1
    }
    if (s.nal_ref_idc != 0) {
        if (s.idr) {
            w.bit(false);  // no_output_of_prior_pics_flag
            w.bit(false);  // long_term_reference_flag
        } else {
            w.bit(false);  // adaptive_ref_pic_marking_mode_flag
        }
    }
    w.byte_align_zero();
    // Stand-in slice data keeps the NAL non-empty and the header from
    // looking truncated to the analyzer.
    std::vector<std::uint8_t> bytes = w.bytes();
    bytes.push_back(0x00);
    bytes.push_back(0x80);
    return bytes;
}

std::uint8_t slice_header_byte(int nal_ref_idc, bool idr) {
    return static_cast<std::uint8_t>((nal_ref_idc << 5) | (idr ? 5 : 1));
}

}  // namespace samplegen
}  // namespace h264inspect
