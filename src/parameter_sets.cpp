#include "parameter_sets.hpp"

#include <limits>

namespace h264 {
namespace {

constexpr unsigned kMaxSpsId = 31;
constexpr unsigned kMaxPpsId = 255;
constexpr unsigned kMaxMbDimension = 256;  // H.264 level limits PicWidthInMbs

// Cropping unit widths (in luma samples) per chroma_format_idc, Table 6-1.
struct CropUnits {
    unsigned sub_width_c;
    unsigned sub_height_c;
};

CropUnits crop_units_for(unsigned chroma_format_idc) {
    switch (chroma_format_idc) {
        case 1: return {2, 2};  // 4:2:0
        case 2: return {2, 1};  // 4:2:2
        case 3: return {1, 1};  // 4:4:4
        default: return {1, 1}; // monochrome / reserved
    }
}

bool validate_and_derive(SpsInfo& s, std::uint64_t offset, Diagnostics& diag) {
    if (s.chroma_format_idc > 3) {
        diag.add(offset, "SPS: chroma_format_idc " +
                             std::to_string(s.chroma_format_idc) + " reserved");
        return false;
    }
    if (s.bit_depth_luma_minus8 > 6 || s.bit_depth_chroma_minus8 > 6) {
        diag.add(offset, "SPS: bit depth outside 8..14 bit range");
        return false;
    }
    if (s.pic_width_in_mbs_minus1 >= kMaxMbDimension ||
        s.pic_height_in_map_units_minus1 >= kMaxMbDimension) {
        diag.add(offset, "SPS: frame dimensions exceed H.264 limits");
        return false;
    }
    if (s.log2_max_frame_num_minus4 > 12 ||
        s.log2_max_pic_order_cnt_lsb_minus4 > 12) {
        diag.add(offset, "SPS: log2 field width exceeds 16 bits");
        return false;
    }

    const std::uint64_t width_mbs =
        static_cast<std::uint64_t>(s.pic_width_in_mbs_minus1) + 1ull;
    const std::uint64_t height_maps =
        static_cast<std::uint64_t>(s.pic_height_in_map_units_minus1) + 1ull;
    const std::uint64_t frame_height_mbs =
        s.frame_mbs_only_flag ? height_maps : 2ull * height_maps;

    std::uint64_t width = width_mbs * 16ull;
    std::uint64_t height = frame_height_mbs * 16ull;

    if (s.frame_cropping_flag) {
        const CropUnits cu = crop_units_for(s.chroma_format_idc);
        const std::uint64_t sx = s.crop_left + s.crop_right;
        const std::uint64_t sy = s.crop_top + s.crop_bottom;
        const std::uint64_t crop_x = sx * cu.sub_width_c;
        const std::uint64_t crop_y = sy * cu.sub_height_c *
                                     (s.frame_mbs_only_flag ? 1ull : 2ull);
        if (crop_x >= width || crop_y >= height) {
            diag.add(offset, "SPS: frame cropping removes the entire picture");
            return false;
        }
        width -= crop_x;
        height -= crop_y;
    }
    s.width = width;
    s.height = height;
    return true;
}

}  // namespace

bool parse_sps(const NalUnit& nal, SpsInfo& s, Diagnostics& diag) {
    if (nal.payload.size() < 2) {
        diag.add(nal.nal_offset, "SPS: NAL unit too short for header");
        return false;
    }
    try {
        BitReader br(nal.payload.data() + 1, nal.payload.size() - 1,
                     nal.nal_offset);
        s.profile_idc = br.read_bits(8);
        s.constraint_flags = br.read_bits(8);
        s.level_idc = br.read_bits(8);
        s.sps_id = read_ue(br);
        if (s.sps_id > kMaxSpsId) {
            diag.add(nal.nal_offset, "SPS: sps_id out of range (0..31)");
            return false;
        }

        if (s.profile_idc == 100 || s.profile_idc == 110 ||
            s.profile_idc == 122 || s.profile_idc == 244 ||
            s.profile_idc == 44 || s.profile_idc == 83 ||
            s.profile_idc == 86 || s.profile_idc == 118 ||
            s.profile_idc == 128 || s.profile_idc == 138 ||
            s.profile_idc == 139 || s.profile_idc == 134 ||
            s.profile_idc == 135) {
            s.chroma_format_idc = read_ue(br);
            if (s.chroma_format_idc == 3) {
                br.read_bit();  // separate_colour_plane_flag
            }
            s.bit_depth_luma_minus8 = read_ue(br);
            s.bit_depth_chroma_minus8 = read_ue(br);
            br.read_bit();  // qpprime_y_zero_transform_bypass_flag
            const bool seq_scaling_matrix_present = br.read_bit() != 0;
            if (seq_scaling_matrix_present) {
                const unsigned lists = s.chroma_format_idc != 3 ? 8u : 12u;
                for (unsigned i = 0; i < lists; ++i) {
                    if (br.read_bit() != 0) {  // seq_scaling_list_present_flag
                        const unsigned size_of_scaling_list = i < 6 ? 16u : 64u;
                        int last_scale = 8, next_scale = 8;
                        for (unsigned j = 0;
                             j < size_of_scaling_list && next_scale != 0; ++j) {
                            if (next_scale != 0) {
                                const int delta = read_se(br);
                                next_scale = (last_scale + delta + 256) % 256;
                            }
                            last_scale = next_scale == 0 ? last_scale : next_scale;
                        }
                    }
                }
            }
        }

        s.log2_max_frame_num_minus4 = read_ue(br);
        s.pic_order_cnt_type = read_ue(br);
        if (s.pic_order_cnt_type == 0) {
            s.log2_max_pic_order_cnt_lsb_minus4 = read_ue(br);
        } else if (s.pic_order_cnt_type == 1) {
            br.read_bit();  // delta_pic_order_always_zero_flag
            (void)read_se(br);  // offset_for_non_ref_pic
            (void)read_se(br);  // offset_for_top_to_bottom_field
            const unsigned n = read_ue(br);
            if (n > 255) {
                diag.add(nal.nal_offset,
                         "SPS: num_ref_frames_in_pic_order_cnt_cycle > 255");
                return false;
            }
            for (unsigned i = 0; i < n; ++i) {
                (void)read_se(br);
            }
        } else if (s.pic_order_cnt_type > 2) {
            diag.add(nal.nal_offset, "SPS: reserved pic_order_cnt_type");
            return false;
        }

        s.max_num_ref_frames = read_ue(br);
        s.gaps_in_frame_num_allowed = br.read_bit() != 0;
        s.pic_width_in_mbs_minus1 = read_ue(br);
        s.pic_height_in_map_units_minus1 = read_ue(br);
        s.frame_mbs_only_flag = br.read_bit() != 0;
        if (!s.frame_mbs_only_flag) {
            s.mb_adaptive_frame_field_flag = br.read_bit() != 0;
        }
        br.read_bit();  // direct_8x8_inference_flag
        s.frame_cropping_flag = br.read_bit() != 0;
        if (s.frame_cropping_flag) {
            s.crop_left = read_ue(br);
            s.crop_right = read_ue(br);
            s.crop_top = read_ue(br);
            s.crop_bottom = read_ue(br);
        }
        // vui_parameters follow and are intentionally not decoded.
        return validate_and_derive(s, nal.nal_offset, diag);
    } catch (const ParseError& e) {
        diag.add(e.byte_offset(), std::string("SPS syntax error: ") + e.what());
        return false;
    }
}

bool parse_pps(const NalUnit& nal, PpsInfo& p, Diagnostics& diag) {
    if (nal.payload.size() < 2) {
        diag.add(nal.nal_offset, "PPS: NAL unit too short for header");
        return false;
    }
    try {
        BitReader br(nal.payload.data() + 1, nal.payload.size() - 1,
                     nal.nal_offset);
        p.pps_id = read_ue(br);
        if (p.pps_id > kMaxPpsId) {
            diag.add(nal.nal_offset, "PPS: pps_id out of range (0..255)");
            return false;
        }
        p.sps_id = read_ue(br);
        if (p.sps_id > kMaxSpsId) {
            diag.add(nal.nal_offset, "PPS: sps_id out of range (0..31)");
            return false;
        }
        p.entropy_coding_mode_flag = br.read_bit() != 0;
        p.bottom_field_pic_order_in_frame_present = br.read_bit() != 0;
        p.num_slice_groups_minus1 = read_ue(br);
        if (p.num_slice_groups_minus1 > 7) {
            diag.add(nal.nal_offset, "PPS: unsupported num_slice_groups > 8");
            return false;
        }
        if (p.num_slice_groups_minus1 > 0) {
            const unsigned map_type = read_ue(br);
            if (map_type == 3 || map_type == 4) {
                const unsigned runs = map_type == 3
                    ? p.num_slice_groups_minus1 + 1
                    : 2 * (p.num_slice_groups_minus1 + 1);
                for (unsigned i = 0; i < runs; ++i) (void)read_ue(br);
            } else if (map_type > 2) {
                (void)read_ue(br);  // slice_group_change_direction_flag region
            }
        }
        p.num_ref_idx_l0_default_active_minus1 = read_ue(br);
        p.num_ref_idx_l1_default_active_minus1 = read_ue(br);
        p.weighted_pred_flag = br.read_bit() != 0;
        p.weighted_bipred_idc = br.read_bits(2);
        p.pic_init_qp_minus26 = read_se(br);
        p.pic_init_qs_minus26 = read_se(br);
        p.chroma_qp_index_offset = read_se(br);
        p.deblocking_filter_control_present = br.read_bit() != 0;
        p.redundant_pic_cnt_present = br.read_bit() != 0;
        if (p.num_ref_idx_l0_default_active_minus1 > 31 ||
            p.num_ref_idx_l1_default_active_minus1 > 31) {
            diag.add(nal.nal_offset, "PPS: reference index count out of range");
            return false;
        }
        return true;
    } catch (const ParseError& e) {
        diag.add(e.byte_offset(), std::string("PPS syntax error: ") + e.what());
        return false;
    }
}

}  // namespace h264
