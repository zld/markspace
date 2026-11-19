#include "h264_parser.hpp"

#include "bit_reader.hpp"

#include <string>

namespace h264 {
namespace {

// Bounds-checked payload view with emulation prevention bytes removed.
std::vector<std::uint8_t> payload_rbsp_sps(const NalUnit& nal,
                                           const std::uint8_t* stream) {
    return rbsp_unescape(stream + nal.payload_offset, nal.payload_size);
}

}  // namespace

SpsInfo parse_sps(const NalUnit& nal, const std::uint8_t* stream,
                  DiagnosticSink& diag) {
    SpsInfo sps;
    sps.present = true;
    auto rbsp = payload_rbsp_sps(nal, stream);
    BitReader br(rbsp.data(), rbsp.size());

    uint32_t profile = 0, level = 0, sps_id = 0;
    uint32_t chroma = 1;
    if (!br.read_bits(8, profile) || !br.read_bits(8, level) ||
        !br.read_ue(sps_id)) {
        diag.error(nal.start_offset, "sps_truncated",
                   "SPS ended before profile/level/set id");
        return sps;
    }
    sps.profile_idc = profile;
    sps.level_idc = level;
    sps.seq_parameter_set_id = sps_id;

    if (profile == 100 || profile == 110 || profile == 122 ||
        profile == 244 || profile == 44 || profile == 83 ||
        profile == 86 || profile == 118 || profile == 128 ||
        profile == 138 || profile == 139 || profile == 134 ||
        profile == 135) {
        uint32_t scaling_present = 0;
        uint32_t bit_depth_luma = 0;
        uint32_t bit_depth_chroma = 0;
        uint32_t qpprime_bypass = 0;
        uint32_t separate_colour = 0;
        if (!br.read_ue(chroma) ||
            !br.read_bits(1, separate_colour) ||
            !br.read_ue(bit_depth_luma) ||
            !br.read_ue(bit_depth_chroma) ||
            !br.read_bits(1, qpprime_bypass) ||
            !br.read_bits(1, scaling_present)) {
            diag.error(nal.start_offset, "sps_truncated",
                       "high-profile SPS extension is truncated");
            return sps;
        }
        if (separate_colour != 0) {
            diag.error(nal.start_offset, "sps_unsupported",
                       "separate colour plane (4:4:4) is not supported");
            return sps;
        }
        if (scaling_present != 0) {
            diag.error(nal.start_offset, "sps_unsupported",
                       "scaling lists are outside the supported SPS subset");
            return sps;
        }
    }

    uint32_t log2_max_frame_num_minus4 = 0;
    uint32_t pic_order_cnt_type = 0;
    uint32_t width_in_mbs_minus1 = 0;
    uint32_t height_in_map_units_minus1 = 0;
    uint32_t frame_mbs_only = 0;
    if (!br.read_ue(log2_max_frame_num_minus4) ||
        !br.read_ue(pic_order_cnt_type)) {
        diag.error(nal.start_offset, "sps_truncated",
                   "SPS ended before picture order control fields");
        return sps;
    }
    sps.log2_max_frame_num_minus4 = log2_max_frame_num_minus4;
    if (pic_order_cnt_type == 0) {
        uint32_t log2_max_pic_order_cnt_lsb_minus4 = 0;
        if (!br.read_ue(log2_max_pic_order_cnt_lsb_minus4)) {
            diag.error(nal.start_offset, "sps_truncated",
                       "SPS missing pic_order_cnt_lsb length");
            return sps;
        }
    } else if (pic_order_cnt_type == 1) {
        diag.error(nal.start_offset, "sps_unsupported",
                   "pic_order_cnt_type 1 is not supported");
        return sps;
    }

    uint32_t max_num_ref_frames = 0;
    uint32_t gaps_in_frame_num_allowed = 0;
    uint32_t pic_width_in_mbs_minus1 = 0;
    if (!br.read_ue(max_num_ref_frames) ||
        !br.read_bits(1, gaps_in_frame_num_allowed) ||
        !br.read_ue(pic_width_in_mbs_minus1) ||
        !br.read_ue(height_in_map_units_minus1) ||
        !br.read_bits(1, frame_mbs_only)) {
        diag.error(nal.start_offset, "sps_truncated",
                   "SPS ended before frame dimension fields");
        return sps;
    }
    sps.frame_mbs_only_flag = frame_mbs_only != 0;
    if (!sps.frame_mbs_only_flag && !br.skip_bits(1)) {
        diag.error(nal.start_offset, "sps_truncated",
                   "SPS missing mb_adaptive_frame_field_flag");
        return sps;
    }

    uint32_t direct_8x8 = 0, crop_flag = 0;
    if (!br.read_bits(1, direct_8x8) ||
        !br.read_bits(1, crop_flag)) {
        diag.error(nal.start_offset, "sps_truncated",
                   "SPS ended before frame cropping fields");
        return sps;
    }
    (void)width_in_mbs_minus1;
    if (crop_flag) {
        if (!br.read_ue(sps.crop_left) || !br.read_ue(sps.crop_right) ||
            !br.read_ue(sps.crop_top) || !br.read_ue(sps.crop_bottom)) {
            diag.error(nal.start_offset, "sps_truncated",
                       "SPS frame cropping values are truncated");
            return sps;
        }
    }

    const std::uint32_t crop_unit_x = (chroma == 0) ? 1u : 2u;
    const std::uint32_t crop_unit_y =
        (chroma == 0) ? (2u - static_cast<std::uint32_t>(
                                  sps.frame_mbs_only_flag))
                      : (4u - 2u * static_cast<std::uint32_t>(
                                       sps.frame_mbs_only_flag));
    const std::uint64_t width_mbs =
        static_cast<std::uint64_t>(pic_width_in_mbs_minus1) + 1;
    const std::uint64_t height_map =
        static_cast<std::uint64_t>(height_in_map_units_minus1) + 1;
    sps.width = static_cast<std::uint32_t>(
        width_mbs * 16 -
        static_cast<std::uint64_t>(crop_unit_x) *
            (static_cast<std::uint64_t>(sps.crop_left) + sps.crop_right));
    sps.height = static_cast<std::uint32_t>(
        height_map * 16 * (sps.frame_mbs_only_flag ? 1u : 2u) -
        static_cast<std::uint64_t>(crop_unit_y) *
            (static_cast<std::uint64_t>(sps.crop_top) + sps.crop_bottom));
    sps.valid = true;
    return sps;
}

PpsInfo parse_pps(const NalUnit& nal, const std::uint8_t* stream,
                  DiagnosticSink& diag) {
    PpsInfo pps;
    pps.present = true;
    auto rbsp = payload_rbsp_sps(nal, stream);
    BitReader br(rbsp.data(), rbsp.size());
    if (!br.read_ue(pps.pic_parameter_set_id) ||
        !br.read_ue(pps.seq_parameter_set_id)) {
        diag.error(nal.start_offset, "pps_truncated",
                   "PPS ended before its parameter set identifiers");
        pps.present = false;
        return pps;
    }
    return pps;
}

}  // namespace h264
