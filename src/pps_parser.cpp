#include "pps_parser.hpp"

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
            diagnostic->message = "truncated PPS flag";
        }
        return false;
    }
    return true;
}

}  // namespace

bool parse_pps(const std::uint8_t* payload, std::size_t payload_size,
               std::uint64_t payload_offset, std::uint64_t nal_offset,
               ExpGolombStats& stats, Pps& out, Diagnostic* diagnostic) {
    out = Pps();
    out.nal_offset = nal_offset;
    out.pps_byte_offset = payload_offset;

    RbspBuffer rbsp;
    if (!build_rbsp(payload, payload_size, payload_offset, rbsp, diagnostic)) {
        return false;
    }
    BitReader reader(rbsp);
    ExpGolombDecoder decoder(reader, stats);
    std::uint32_t value = 0;

    if (!decoder.read_ue_u32(out.id, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid pic_parameter_set_id");
    }
    if (!decoder.read_ue_u32(out.sps_id, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid seq_parameter_set_id");
    }
    if (!read_flag(reader, out.entropy_coding_mode, diagnostic)) return false;
    if (!read_flag(reader, out.bottom_field_pic_order_in_frame_present, diagnostic)) {
        return false;
    }
    if (!decoder.read_ue_u32(out.num_slice_groups_minus1, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid num_slice_groups_minus1");
    }
    if (out.num_slice_groups_minus1 != 0) {
        // FMO changes slice-group mapping semantics; out of analyzer scope.
        return fail(diagnostic, ErrorKind::Unsupported, reader.current_offset(),
                    "slice groups (FMO) are not supported");
    }
    if (!decoder.read_ue_u32(out.num_ref_idx_l0_default_active, diagnostic) ||
        !decoder.read_ue_u32(out.num_ref_idx_l1_default_active, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid default active reference counts");
    }
    out.num_ref_idx_l0_default_active += 1;
    out.num_ref_idx_l1_default_active += 1;
    if (!read_flag(reader, out.weighted_pred, diagnostic)) return false;
    if (!reader.read_bits(2, value)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid weighted_bipred_idc");
    }
    out.weighted_bipred_idc = value;
    if (out.weighted_bipred_idc > 2) {
        return fail(diagnostic, ErrorKind::IllegalValue, reader.current_offset(),
                    "weighted_bipred_idc exceeds 2");
    }
    if (!decoder.read_se(out.pic_init_qp_minus26, diagnostic) ||
        !decoder.read_se(out.pic_init_qs_minus26, diagnostic) ||
        !decoder.read_se(out.chroma_qp_index_offset, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "invalid PPS QP offset fields");
    }
    if (!read_flag(reader, out.deblocking_filter_control_present, diagnostic)) {
        return false;
    }
    if (!read_flag(reader, out.constrained_intra_pred, diagnostic)) return false;
    if (!read_flag(reader, out.redundant_pic_cnt_present, diagnostic)) return false;

    const std::size_t remaining = reader.bits_remaining();
    if (remaining == 0) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "PPS ends before rbsp_trailing_bits");
    }
    return reader.read_rbsp_trailing_bits(diagnostic);
}

}  // namespace h264inspect
