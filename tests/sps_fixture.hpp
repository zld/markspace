// SPS fixture synthesis for tests and sample generation.
#pragma once

#include <cstdint>
#include <vector>

#include "annexb_fixture.hpp"
#include "bit_writer.hpp"

namespace selftest {

struct SpsFixture {
    std::uint32_t profile_idc = 100;  // High profile exercises chroma/bit-depth
    std::uint32_t level_idc = 31;
    std::uint32_t id = 0;
    std::uint32_t chroma_format_idc = 1;
    bool separate_colour_plane = false;
    std::uint32_t bit_depth_luma_minus8 = 0;
    std::uint32_t bit_depth_chroma_minus8 = 0;
    bool qpprime_bypass = false;
    bool scaling_matrix = false;

    std::uint32_t log2_max_frame_num_minus4 = 0;
    std::uint32_t pic_order_cnt_type = 0;
    std::uint32_t log2_max_pic_order_cnt_lsb_minus4 = 2;
    bool delta_pic_order_always_zero = false;
    std::int32_t offset_for_non_ref_pic = 0;
    std::int32_t offset_for_top_to_bottom_field = 0;
    std::vector<std::int32_t> offset_for_ref_frame;
    std::uint32_t max_num_ref_frames = 4;
    bool gaps_in_frame_num_allowed = false;

    std::uint32_t pic_width_in_mbs_minus1 = 19;         // 320 px
    std::uint32_t pic_height_in_map_units_minus1 = 11;  // 192 px (frame map)
    bool frame_mbs_only = true;
    bool mb_adaptive_frame_field = false;
    bool direct_8x8_inference = true;

    bool crop = false;
    std::uint32_t crop_left = 0;
    std::uint32_t crop_right = 0;
    std::uint32_t crop_top = 0;
    std::uint32_t crop_bottom = 0;

    bool vui = false;
};

// Builds the SPS RBSP (without NAL header) according to `f`.
inline std::vector<std::uint8_t> build_sps_rbsp(const SpsFixture& f) {
    BitWriter w;
    w.bits(f.profile_idc, 8);
    w.bits(0, 8);  // constraint flags
    w.bits(f.level_idc, 8);
    w.ue(f.id);
    const bool high = f.profile_idc == 100 || f.profile_idc == 110 ||
                      f.profile_idc == 122 || f.profile_idc == 244;
    if (high) {
        w.ue(f.chroma_format_idc);
        if (f.chroma_format_idc == 3) w.bit(f.separate_colour_plane);
        w.ue(f.bit_depth_luma_minus8);
        w.ue(f.bit_depth_chroma_minus8);
        w.bit(f.qpprime_bypass);
        w.bit(f.scaling_matrix);
        if (f.scaling_matrix) {
            const int count = f.chroma_format_idc != 3 ? 8 : 12;
            for (int i = 0; i < count; ++i) w.bit(false);
        }
    }
    w.ue(f.log2_max_frame_num_minus4);
    w.ue(f.pic_order_cnt_type);
    if (f.pic_order_cnt_type == 0) {
        w.ue(f.log2_max_pic_order_cnt_lsb_minus4);
    } else if (f.pic_order_cnt_type == 1) {
        w.bit(f.delta_pic_order_always_zero);
        w.se(f.offset_for_non_ref_pic);
        w.se(f.offset_for_top_to_bottom_field);
        w.ue(static_cast<std::uint64_t>(f.offset_for_ref_frame.size()));
        for (std::int32_t v : f.offset_for_ref_frame) w.se(v);
    }
    w.ue(f.max_num_ref_frames);
    w.bit(f.gaps_in_frame_num_allowed);
    w.ue(f.pic_width_in_mbs_minus1);
    w.ue(f.pic_height_in_map_units_minus1);
    w.bit(f.frame_mbs_only);
    if (!f.frame_mbs_only) w.bit(f.mb_adaptive_frame_field);
    w.bit(f.direct_8x8_inference);
    w.bit(f.crop);
    if (f.crop) {
        w.ue(f.crop_left);
        w.ue(f.crop_right);
        w.ue(f.crop_top);
        w.ue(f.crop_bottom);
    }
    w.bit(f.vui);
    if (f.vui) {
        w.bit(false);  // aspect_ratio_info_present_flag
    }
    w.rbsp_trailing_bits();
    return w.bytes();
}

// Builds a complete Annex B SPS NAL with emulation prevention applied.
inline std::vector<std::uint8_t> build_sps_nal(const SpsFixture& f,
                                               bool four_byte = true) {
    return annexb_nal(0x67, escape_rbsp(build_sps_rbsp(f)), four_byte);
}

}  // namespace selftest
