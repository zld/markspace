// Sequence parameter set fields and derived geometry helpers.
#pragma once

#include <cstdint>
#include <string>

#include "bit_reader.hpp"
#include "exp_golomb.hpp"
#include "nal_parser.hpp"

namespace h264inspect {

constexpr std::uint32_t kMaxDimension = 16384;
constexpr std::uint32_t kMaxBitDepth = 16;

struct Sps {
    std::uint32_t id = 0;
    std::uint32_t profile_idc = 0;
    std::uint32_t constraint_flags = 0;  // constraint_set0..5 + reserved bits
    std::uint32_t level_idc = 0;

    bool separate_colour_plane_flag = false;
    std::uint32_t chroma_format_idc = 1;  // 0=mono, 1=4:2:0, 2=4:2:2, 3=4:4:4
    std::uint32_t bit_depth_luma = 8;
    std::uint32_t bit_depth_chroma = 8;
    bool qpprime_y_zero_transform_bypass = false;
    bool seq_scaling_matrix_present = false;

    std::uint32_t log2_max_frame_num = 0;  // log2_max_frame_num_minus4 + 4
    std::uint32_t pic_order_cnt_type = 0;
    std::uint32_t log2_max_pic_order_cnt_lsb = 0;  // +4, only for type 0
    bool delta_pic_order_always_zero = false;
    std::int64_t offset_for_non_ref_pic = 0;
    std::int64_t offset_for_top_to_bottom_field = 0;
    std::uint32_t num_ref_frames_in_pic_order_cnt_cycle = 0;
    std::uint64_t max_num_ref_frames = 0;
    bool gaps_in_frame_num_value_allowed = false;

    std::uint32_t pic_width_in_mbs = 0;
    std::uint32_t pic_height_in_map_units = 0;
    bool frame_mbs_only_flag = true;
    bool mb_adaptive_frame_field_flag = false;
    bool direct_8x8_inference_flag = false;

    bool frame_cropping_flag = false;
    std::uint32_t crop_left = 0;
    std::uint32_t crop_right = 0;
    std::uint32_t crop_top = 0;
    std::uint32_t crop_bottom = 0;

    bool vui_parameters_present = false;

    // Derived values. `width`/`height` are the cropped display dimensions.
    std::uint32_t width = 0;
    std::uint32_t height = 0;
    std::uint32_t coded_width = 0;
    std::uint32_t coded_height = 0;
    bool interlaced = false;  // !frame_mbs_only_flag

    std::uint64_t nal_offset = 0;   // start-code offset of the carrying NAL
    std::uint64_t sps_byte_offset = 0;  // offset just past the NAL header

    std::uint32_t ChromaArrayType() const {
        return separate_colour_plane_flag ? 0 : chroma_format_idc;
    }
    std::uint32_t SubWidthC() const {
        return chroma_format_idc == 1 || chroma_format_idc == 2 ? 2 : 1;
    }
    std::uint32_t SubHeightC() const {
        return chroma_format_idc == 1 ? 2 : 1;
    }
};

}  // namespace h264inspect
