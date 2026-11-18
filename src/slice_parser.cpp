#include "slice_parser.hpp"

#include "slice_header_lists.hpp"

#include <string>
#include <vector>

namespace h264inspect {

namespace {

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

bool read_flag(BitReader& reader, bool& out, Diagnostic* diagnostic) {
    if (!reader.read_bit(out)) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Truncated;
            diagnostic->offset = reader.current_offset();
            diagnostic->message = "truncated slice header flag";
        }
        return false;
    }
    return true;
}

}  // namespace

bool read_slice_prefix(const std::uint8_t* payload, std::size_t payload_size,
                       std::uint64_t payload_offset, ExpGolombStats& stats,
                       SlicePrefix& out) {
    RbspBuffer rbsp;
    Diagnostic diag;
    if (!build_rbsp(payload, payload_size, payload_offset, rbsp, &diag)) {
        return false;
    }
    BitReader reader(rbsp);
    ExpGolombDecoder decoder(reader, stats);
    return decoder.read_ue_u32(out.first_mb, &diag) &&
           decoder.read_ue_u32(out.slice_type_raw, &diag) &&
           decoder.read_ue_u32(out.pps_id, &diag);
}

bool parse_slice_header(const std::uint8_t* payload, std::size_t payload_size,
                        std::uint64_t payload_offset, int nal_ref_idc,
                        int nal_unit_type, const SliceContext& context,
                        ExpGolombStats& stats, SliceHeader& out,
                        Diagnostic* diagnostic) {
    out = SliceHeader();
    out.is_idr = nal_unit_type == 5;
    out.nal_ref_idc_zero = nal_ref_idc == 0;

    RbspBuffer rbsp;
    if (!build_rbsp(payload, payload_size, payload_offset, rbsp, diagnostic)) {
        return false;
    }
    BitReader reader(rbsp);
    ExpGolombDecoder decoder(reader, stats);

    if (!decoder.read_ue_u32(out.first_mb_in_slice, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid first_mb_in_slice");
    }
    std::uint32_t raw_type = 0;
    if (!decoder.read_ue_u32(raw_type, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid slice_type");
    }
    if (raw_type > 9) {
        return fail(diagnostic, ErrorKind::Unsupported, reader.current_offset(),
                    "slice_type " + std::to_string(raw_type) + " is out of range");
    }
    out.slice_type = static_cast<SliceType>(raw_type % 5);
    if (!decoder.read_ue_u32(out.pps_id, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid pic_parameter_set_id");
    }
    if (context.sps == nullptr || context.pps == nullptr) {
        return fail(diagnostic, ErrorKind::UnknownPps, reader.current_offset(),
                    "no active parameter set for slice header");
    }
    const Sps& sps = *context.sps;
    const Pps& pps = *context.pps;
    out.sps_id = sps.id;
    if (pps.sps_id != sps.id) {
        return fail(diagnostic, ErrorKind::IllegalValue, reader.current_offset(),
                    "PPS references SPS " + std::to_string(pps.sps_id) +
                        " but SPS " + std::to_string(sps.id) + " is active");
    }
    if (sps.separate_colour_plane_flag) {
        std::uint32_t colour_plane = 0;
        if (!reader.read_bits(2, colour_plane)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated colour_plane_id");
        }
        if (colour_plane > 2) {
            return fail(diagnostic, ErrorKind::IllegalValue, reader.current_offset(),
                        "colour_plane_id exceeds 2");
        }
    }

    if (!reader.read_bits(static_cast<int>(sps.log2_max_frame_num), out.frame_num)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "truncated frame_num");
    }
    out.have_frame_num = true;
    if (!sps.frame_mbs_only_flag) {
        if (!read_flag(reader, out.field_pic_flag, diagnostic)) return false;
        if (out.field_pic_flag) {
            if (!read_flag(reader, out.bottom_field_flag, diagnostic)) return false;
        }
    }
    if (out.is_idr) {
        if (!decoder.read_ue_u32(out.idr_pic_id, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated idr_pic_id");
        }
    }
    if (sps.pic_order_cnt_type == 0) {
        if (!reader.read_bits(static_cast<int>(sps.log2_max_pic_order_cnt_lsb),
                              out.pic_order_cnt_lsb)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated pic_order_cnt_lsb");
        }
        out.have_poc_lsb = true;
        if (pps.bottom_field_pic_order_in_frame_present && !out.field_pic_flag) {
            if (!decoder.read_se(out.delta_pic_order_cnt_bottom, diagnostic)) {
                return fail(diagnostic, ErrorKind::Truncated,
                            reader.current_offset(),
                            "truncated delta_pic_order_cnt_bottom");
            }
        }
    } else if (sps.pic_order_cnt_type == 1 && !sps.delta_pic_order_always_zero) {
        if (!decoder.read_se(out.delta_pic_order_cnt_0, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated delta_pic_order_cnt[0]");
        }
        if (pps.bottom_field_pic_order_in_frame_present && !out.field_pic_flag) {
            if (!decoder.read_se(out.delta_pic_order_cnt_1, diagnostic)) {
                return fail(diagnostic, ErrorKind::Truncated,
                            reader.current_offset(),
                            "truncated delta_pic_order_cnt[1]");
            }
        }
    }

    if (pps.redundant_pic_cnt_present) {
        std::uint32_t redundant = 0;
        if (!decoder.read_ue_u32(redundant, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated redundant_pic_cnt");
        }
    }

    const std::uint32_t type = static_cast<std::uint32_t>(out.slice_type);
    if (type == static_cast<std::uint32_t>(SliceType::B)) {
        bool direct = false;
        if (!read_flag(reader, direct, diagnostic)) return false;
    }
    if (type != static_cast<std::uint32_t>(SliceType::I) &&
        type != static_cast<std::uint32_t>(SliceType::Si)) {
        std::uint32_t override_count = 0;
        if (!reader.read_bits(1, override_count)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated num_ref_idx_active_override_flag");
        }
        if (override_count) {
            std::uint32_t l0 = 0;
            if (!decoder.read_ue_u32(l0, diagnostic)) {
                return fail(diagnostic, ErrorKind::Truncated,
                            reader.current_offset(),
                            "truncated num_ref_idx_l0_active_minus1");
            }
            if (type == static_cast<std::uint32_t>(SliceType::B)) {
                std::uint32_t l1 = 0;
                if (!decoder.read_ue_u32(l1, diagnostic)) {
                    return fail(diagnostic, ErrorKind::Truncated,
                                reader.current_offset(),
                                "truncated num_ref_idx_l1_active_minus1");
                }
            }
        }
        // P/SP use list 0; B uses list 0 and list 1.
        const std::uint32_t lists =
            type == static_cast<std::uint32_t>(SliceType::B) ? 2u : 1u;
        for (std::uint32_t i = 0; i < lists; ++i) {
            if (!skip_ref_pic_list_modification(reader, decoder, diagnostic)) {
                return false;
            }
        }
    }
    if ((pps.weighted_pred && type == static_cast<std::uint32_t>(SliceType::P)) ||
        (pps.weighted_bipred_idc == 1 &&
         type == static_cast<std::uint32_t>(SliceType::B))) {
        if (!skip_pred_weight_table(reader, decoder, sps, pps, type, diagnostic)) {
            return false;
        }
    }
    if (nal_ref_idc != 0) {
        if (!skip_dec_ref_pic_marking(reader, decoder, out.is_idr, diagnostic)) {
            return false;
        }
    }

    // The remaining header fields (CABAC init, deblocking, slice groups) do not
    // affect access-unit grouping, so analysis stops here by design. If the
    // payload ended exactly at this point the slice data is absent, which is
    // reported by the caller rather than treated as a hard failure.
    out.header_complete = reader.bits_remaining() >= 8;
    return true;
}

}  // namespace h264inspect