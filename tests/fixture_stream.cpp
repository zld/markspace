#include "bit_writer.hpp"

namespace h264::gen {
namespace {

constexpr std::uint8_t kNalSps = 7;
constexpr std::uint8_t kNalPps = 8;
constexpr std::uint8_t kNalIdr = 5;
constexpr std::uint8_t kNalNonIdr = 1;

// Slice type codes: 2=I, 0=P, 1=B (non-redundant forms).
constexpr std::uint32_t kSliceI = 2;
constexpr std::uint32_t kSliceP = 0;
constexpr std::uint32_t kSliceB = 1;

std::vector<std::uint8_t> make_sps() {
    BitWriter w;
    w.write_bits(100, 8);   // profile_idc: High, exercises high-profile path
    w.write_bits(40, 8);    // level_idc 4.0
    w.write_ue(0);          // seq_parameter_set_id
    w.write_ue(1);          // chroma_format_idc = 4:2:0
    w.write_flag(false);    // separate_colour_plane_flag
    w.write_bits(0, 1);     // bit_depth_luma_minus8
    w.write_bits(0, 1);     // bit_depth_chroma_minus8
    w.write_ue(0);          // qpprime_y_zero_transform_bypass_flag
    w.write_ue(0);          // seq_scaling_matrix_present_flag
    w.write_ue(0);          // log2_max_frame_num_minus4 -> 4-bit frame_num
    w.write_ue(0);          // pic_order_cnt_type = 0
    w.write_ue(0);          // log2_max_pic_order_cnt_lsb_minus4
    w.write_ue(4);          // max_num_ref_frames
    w.write_flag(false);    // gaps_in_frame_num_value_allowed_flag
    // 1280 / 16 - 1 = 79 ; 720 / 16 - 1 = 44
    w.write_ue(79);         // pic_width_in_mbs_minus1
    w.write_ue(44);         // pic_height_in_map_units_minus1
    w.write_flag(false);    // frame_mbs_only_flag = 0: fields allowed
    w.write_flag(false);    // mb_adaptive_frame_field_flag
    w.write_flag(true);     // direct_8x8_inference_flag
    w.write_flag(false);    // frame_cropping_flag
    w.write_flag(false);    // vui_parameters_present_flag
    w.align_zero();
    return w.take_escaped();
}

std::vector<std::uint8_t> make_pps() {
    BitWriter w;
    w.write_ue(0);  // pic_parameter_set_id
    w.write_ue(0);  // seq_parameter_set_id
    w.write_flag(false);  // entropy_coding_mode_flag (CAVLC)
    w.write_flag(false);  // bottom_field_pic_order_in_frame_present_flag
    w.write_ue(0);        // num_slice_groups_minus1
    w.write_ue(0);        // num_ref_idx_l0_default_active_minus1
    w.write_ue(0);        // num_ref_idx_l1_default_active_minus1
    w.write_flag(false);  // weighted_pred_flag
    w.write_bits(0, 2);   // weighted_bipred_idc
    w.write_se(0);        // pic_init_qp_minus26
    w.write_se(0);        // pic_init_qs_minus26
    w.write_se(0);        // chroma_qp_index_offset
    w.write_flag(false);  // deblocking_filter_control_present_flag
    w.write_flag(false);  // constrained_intra_pred_flag
    w.write_flag(false);  // redundant_pic_cnt_present_flag
    w.align_zero();
    return w.take_escaped();
}

// Writes the common slice header prefix up to the optional field flags.
void write_slice_header(BitWriter& w, std::uint32_t first_mb,
                        std::uint32_t slice_type, std::uint32_t frame_num,
                        bool field_pic, bool bottom_field, bool idr) {
    w.write_ue(first_mb);   // first_mb_in_slice
    w.write_ue(slice_type);
    w.write_ue(0);          // pic_parameter_set_id
    w.write_bits(frame_num, 4);
    if (field_pic) {
        w.write_flag(true);        // field_pic_flag
        w.write_flag(bottom_field);
    } else {
        w.write_flag(false);       // field_pic_flag
    }
    if (idr) w.write_ue(0);        // idr_pic_id
    // Remaining optional headers are all absent via default PPS/SPS flags;
    // pad with a zero byte so every NAL has non-header payload.
    w.write_bits(0, 8);
    w.align_zero();
}

std::vector<std::uint8_t> make_slice(std::uint32_t first_mb,
                                     std::uint32_t slice_type,
                                     std::uint32_t frame_num,
                                     bool field_pic = false,
                                     bool bottom_field = false,
                                     bool idr = false) {
    BitWriter w;
    write_slice_header(w, first_mb, slice_type, frame_num, field_pic,
                       bottom_field, idr);
    return w.take_escaped();
}

}  // namespace

std::vector<std::uint8_t> build_demo_stream() {
    std::vector<std::uint8_t> out;

    // Leading zero bytes exercise leading_zero_8bits handling.
    out.push_back(0x00);
    append_nal(out, (0x3 << 5) | kNalSps, make_sps(), /*four_byte=*/true);
    append_nal(out, (0x3 << 5) | kNalPps, make_pps(), /*four_byte=*/false);

    struct Pic {
        std::uint32_t frame_num;
        std::uint32_t slice_type;
        bool idr;
        bool field_pair;   // emit top and bottom field as two access units
        unsigned slices;   // extra slices inside one frame picture
    };
    // Two GOPs of six pictures; B pictures sit between reference pictures.
    const Pic gop[2][6] = {
        {{0, kSliceI, true, false, 1},
         {1, kSliceP, false, false, 2},
         {2, kSliceB, false, false, 1},
         {3, kSliceP, false, true, 1},
         {4, kSliceB, false, false, 1},
         {5, kSliceP, false, false, 1}},
        {{6, kSliceI, true, false, 1},
         {7, kSliceP, false, false, 1},
         {8, kSliceB, false, false, 1},
         {9, kSliceP, false, false, 1},
         {10, kSliceB, false, false, 1},
         {11, kSliceP, false, false, 1}},
    };

    for (int g = 0; g < 2; ++g) {
        for (const Pic& pic : gop[g]) {
            const std::uint8_t nal_type = pic.idr ? kNalIdr : kNalNonIdr;
            const std::uint8_t ref_idc = pic.idr ? 3u : (pic.slice_type == kSliceB ? 0u : 2u);
            const std::uint8_t header = (ref_idc << 5) | nal_type;
            bool four = (pic.frame_num % 2 == 0);
            if (pic.field_pair) {
                append_nal(out, header,
                           make_slice(0, pic.slice_type, pic.frame_num,
                                      true, false, pic.idr),
                           four);
                append_nal(out, header,
                           make_slice(0, pic.slice_type, pic.frame_num,
                                      true, true, pic.idr),
                           !four);
            } else {
                for (unsigned s = 0; s < pic.slices; ++s) {
                    append_nal(out, header,
                               make_slice(s * 10, pic.slice_type,
                                          pic.frame_num),
                               four);
                }
            }
        }
    }
    return out;  // final NAL deliberately has no trailing zero bytes
}

}  // namespace h264::gen
