#include "sps_parser.hpp"

#include <string>

#include "sps_dimensions.hpp"

namespace h264inspect {

namespace {

bool is_high_profile(std::uint32_t profile_idc) {
    switch (profile_idc) {
        case 100: case 110: case 122: case 244: case 44: case 83:
        case 86: case 118: case 128: case 138: case 139: case 134: case 135:
            return true;
        default:
            return false;
    }
}

// Reads ue(v) into a bounded unsigned field. Returns false when the codeword
// is missing or wider than 32 bits; stats are updated by the decoder.
bool read_ue32(ExpGolombDecoder& decoder, std::uint32_t& out,
               Diagnostic* diagnostic) {
    return decoder.read_ue_u32(out, diagnostic);
}

bool read_se64(ExpGolombDecoder& decoder, std::int64_t& out,
               Diagnostic* diagnostic) {
    return decoder.read_se(out, diagnostic);
}

bool read_flag(BitReader& reader, bool& out, Diagnostic* diagnostic) {
    if (!reader.read_bit(out)) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Truncated;
            diagnostic->offset = reader.current_offset();
            diagnostic->message = "truncated SPS flag";
        }
        return false;
    }
    return true;
}

bool skip_scaling_list(ExpGolombDecoder& decoder, int size,
                       Diagnostic* diagnostic) {
    std::int64_t last_scale = 8;
    std::int64_t next_scale = 8;
    for (int j = 0; j < size; ++j) {
        if (next_scale != 0) {
            std::int64_t delta = 0;
            if (!read_se64(decoder, delta, diagnostic)) return false;
            next_scale = (last_scale + delta + 256) % 256;
        }
        last_scale = next_scale == 0 ? last_scale : next_scale;
    }
    return true;
}

bool fail(Diagnostic* diagnostic, ErrorKind kind, std::uint64_t offset,
          const std::string& message) {
    if (diagnostic) {
        // The Exp-Golomb decoder classifies its own failures more precisely
        // (truncated vs. over-wide codeword); keep that classification.
        if (diagnostic->kind != ErrorKind::ExpGolomb) {
            diagnostic->kind = kind;
            diagnostic->offset = offset;
            diagnostic->message = message;
        }
    }
    return false;
}

}  // namespace

bool parse_sps(const std::uint8_t* payload, std::size_t payload_size,
               std::uint64_t payload_offset, std::uint64_t nal_offset,
               ExpGolombStats& stats, Sps& out, Diagnostic* diagnostic) {
    out = Sps();
    out.nal_offset = nal_offset;
    out.sps_byte_offset = payload_offset;

    RbspBuffer rbsp;
    if (!build_rbsp(payload, payload_size, payload_offset, rbsp, diagnostic)) {
        return false;
    }
    BitReader reader(rbsp);
    ExpGolombDecoder decoder(reader, stats);
    std::uint32_t value = 0;

    if (!reader.read_bits(8, value)) {
        return fail(diagnostic, ErrorKind::Truncated, payload_offset,
                    "truncated SPS before profile_idc");
    }
    out.profile_idc = value;
    if (!reader.read_bits(8, value)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "truncated SPS before constraint flags");
    }
    out.constraint_flags = value;
    if (!reader.read_bits(8, value)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "truncated SPS before level_idc");
    }
    out.level_idc = value;
    if (!read_ue32(decoder, out.id, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "truncated or invalid seq_parameter_set_id");
    }

    const bool high = is_high_profile(out.profile_idc);
    if (high) {
        if (!read_ue32(decoder, out.chroma_format_idc, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "invalid chroma_format_idc");
        }
        if (out.chroma_format_idc > 3) {
            return fail(diagnostic, ErrorKind::Unsupported, reader.current_offset(),
                        "unsupported chroma_format_idc " +
                            std::to_string(out.chroma_format_idc));
        }
        if (out.chroma_format_idc == 3) {
            if (!read_flag(reader, out.separate_colour_plane_flag, diagnostic)) {
                return false;
            }
        }
        if (!read_ue32(decoder, out.bit_depth_luma, diagnostic) ||
            !read_ue32(decoder, out.bit_depth_chroma, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "invalid bit depth fields");
        }
        out.bit_depth_luma += 8;
        out.bit_depth_chroma += 8;
        if (out.bit_depth_luma > kMaxBitDepth || out.bit_depth_chroma > kMaxBitDepth) {
            return fail(diagnostic, ErrorKind::Unsupported, reader.current_offset(),
                        "unsupported bit depth (luma " +
                            std::to_string(out.bit_depth_luma) + ", chroma " +
                            std::to_string(out.bit_depth_chroma) + ")");
        }
        if (!read_flag(reader, out.qpprime_y_zero_transform_bypass, diagnostic)) {
            return false;
        }
        if (!read_flag(reader, out.seq_scaling_matrix_present, diagnostic)) {
            return false;
        }
        if (out.seq_scaling_matrix_present) {
            const int count = out.chroma_format_idc != 3 ? 8 : 12;
            for (int i = 0; i < count; ++i) {
                bool present = false;
                if (!read_flag(reader, present, diagnostic)) return false;
                if (present && !skip_scaling_list(decoder, i < 6 ? 16 : 64,
                                                  diagnostic)) {
                    return fail(diagnostic, ErrorKind::Truncated,
                                reader.current_offset(), "truncated scaling list");
                }
            }
        }
    } else {
        out.chroma_format_idc = 1;
    }

    if (!read_ue32(decoder, value, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid log2_max_frame_num_minus4");
    }
    out.log2_max_frame_num = value + 4;
    if (!read_ue32(decoder, out.pic_order_cnt_type, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid pic_order_cnt_type");
    }
    if (out.pic_order_cnt_type > 2) {
        return fail(diagnostic, ErrorKind::Unsupported, reader.current_offset(),
                    "unsupported pic_order_cnt_type " +
                        std::to_string(out.pic_order_cnt_type));
    }
    if (out.pic_order_cnt_type == 0) {
        if (!read_ue32(decoder, out.log2_max_pic_order_cnt_lsb, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "invalid log2_max_pic_order_cnt_lsb_minus4");
        }
        out.log2_max_pic_order_cnt_lsb += 4;
    } else if (out.pic_order_cnt_type == 1) {
        if (!read_flag(reader, out.delta_pic_order_always_zero, diagnostic)) {
            return false;
        }
        if (!read_se64(decoder, out.offset_for_non_ref_pic, diagnostic) ||
            !read_se64(decoder, out.offset_for_top_to_bottom_field, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "invalid POC offset fields");
        }
        if (!read_ue32(decoder, out.num_ref_frames_in_pic_order_cnt_cycle,
                       diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "invalid num_ref_frames_in_pic_order_cnt_cycle");
        }
        if (out.num_ref_frames_in_pic_order_cnt_cycle > 255) {
            return fail(diagnostic, ErrorKind::Unsupported, reader.current_offset(),
                        "num_ref_frames_in_pic_order_cnt_cycle exceeds 255");
        }
        for (std::uint32_t i = 0; i < out.num_ref_frames_in_pic_order_cnt_cycle; ++i) {
            std::int64_t ignored = 0;
            if (!read_se64(decoder, ignored, diagnostic)) {
                return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                            "truncated offset_for_ref_frame list");
            }
        }
    }

    if (!decoder.read_ue(out.max_num_ref_frames, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid max_num_ref_frames");
    }
    if (!read_flag(reader, out.gaps_in_frame_num_value_allowed, diagnostic)) {
        return false;
    }
    if (!read_ue32(decoder, out.pic_width_in_mbs, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid pic_width_in_mbs_minus1");
    }
    if (!read_ue32(decoder, out.pic_height_in_map_units, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid pic_height_in_map_units_minus1");
    }
    out.pic_width_in_mbs += 1;
    out.pic_height_in_map_units += 1;
    if (out.pic_width_in_mbs > kMaxDimension / 16 ||
        out.pic_height_in_map_units > kMaxDimension / 16) {
        return fail(diagnostic, ErrorKind::LimitExceeded, reader.current_offset(),
                    "coded picture " +
                        std::to_string(out.pic_width_in_mbs * 16u) + "x" +
                        std::to_string(out.pic_height_in_map_units * 16u) +
                        " exceeds limit " + std::to_string(kMaxDimension));
    }

    if (!read_flag(reader, out.frame_mbs_only_flag, diagnostic)) {
        return false;
    }
    out.interlaced = !out.frame_mbs_only_flag;
    if (!out.frame_mbs_only_flag) {
        if (!read_flag(reader, out.mb_adaptive_frame_field_flag, diagnostic)) {
            return false;
        }
    }
    if (!read_flag(reader, out.direct_8x8_inference_flag, diagnostic)) {
        return false;
    }
    if (!read_flag(reader, out.frame_cropping_flag, diagnostic)) {
        return false;
    }
    if (out.frame_cropping_flag) {
        if (!read_ue32(decoder, out.crop_left, diagnostic) ||
            !read_ue32(decoder, out.crop_right, diagnostic) ||
            !read_ue32(decoder, out.crop_top, diagnostic) ||
            !read_ue32(decoder, out.crop_bottom, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "invalid frame cropping offsets");
        }
    }
    if (!read_flag(reader, out.vui_parameters_present, diagnostic)) {
        return false;
    }

    if (!derive_sps_dimensions(out, diagnostic)) {
        return false;
    }

    // A VUI extension is outside this analyzer's scope. The core SPS is still
    // valid and usable, so retain it without validating the tail.
    if (out.vui_parameters_present) {
        return true;
    }
    return reader.read_rbsp_trailing_bits(diagnostic);
}

}  // namespace h264inspect
