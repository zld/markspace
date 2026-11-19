#include "slice_parser.hpp"

#include "bit_reader.hpp"
#include "exp_golomb.hpp"

namespace h264 {

const char* frame_kind_name(FrameKind k) {
    switch (k) {
        case FrameKind::I: return "I";
        case FrameKind::P: return "P";
        case FrameKind::B: return "B";
        case FrameKind::Unknown: return "unknown";
    }
    return "unknown";
}

namespace {

FrameKind kind_from_slice_type(unsigned raw) {
    const unsigned base = raw >= 5 ? raw - 5 : raw;
    switch (base) {
        case SLICE_P:
        case SLICE_SP: return FrameKind::P;
        case SLICE_B: return FrameKind::B;
        case SLICE_I:
        case SLICE_SI: return FrameKind::I;
        default: return FrameKind::Unknown;
    }
}

}  // namespace

bool parse_slice_header(const NalUnit& nal,
                        const ParameterSetStore& store,
                        SliceHeader& out,
                        Diagnostics& diag) {
    if (nal.payload.size() < 2) {
        diag.add(nal.nal_offset, "slice: NAL unit too short for header");
        return false;
    }
    out.idr_pic_flag = (nal.nal_type == NAL_IDR);
    try {
        BitReader br(nal.payload.data() + 1, nal.payload.size() - 1,
                     nal.nal_offset);
        out.first_mb_in_slice = read_ue(br);
        out.slice_type_raw = read_ue(br);
        if (out.slice_type_raw > 9) {
            diag.add(nal.nal_offset,
                     "slice: unsupported slice_type " +
                         std::to_string(out.slice_type_raw));
            out.kind = FrameKind::Unknown;
            return false;
        }
        out.pps_id = read_ue(br);

        const PpsInfo* pps = store.find_pps(out.pps_id);
        if (pps == nullptr) {
            diag.add(nal.nal_offset,
                     "slice: PPS id " + std::to_string(out.pps_id) +
                         " not defined; cannot resolve picture parameters");
            return false;
        }
        const SpsInfo* sps = store.find_sps(pps->sps_id);
        if (sps == nullptr) {
            diag.add(nal.nal_offset,
                     "slice: SPS id " + std::to_string(pps->sps_id) +
                         " referenced by PPS is not defined");
            return false;
        }

        const unsigned frame_num_bits =
            sps->log2_max_frame_num_minus4 + 4u;
        out.frame_num = br.read_bits(frame_num_bits);

        if (!sps->frame_mbs_only_flag) {
            out.field_pic_flag = br.read_bit() != 0;
            if (out.field_pic_flag) {
                out.bottom_field_flag = br.read_bit() != 0;
            }
        } else {
            out.field_pic_flag = false;
            out.bottom_field_flag = false;
        }

        if (out.idr_pic_flag) {
            out.idr_pic_id = read_ue(br);
        }

        out.kind = kind_from_slice_type(out.slice_type_raw);
        out.parse_ok = true;
        return true;
    } catch (const ParseError& e) {
        diag.add(e.byte_offset(),
                 std::string("slice header syntax error: ") + e.what());
        out.kind = FrameKind::Unknown;
        return false;
    }
}

}  // namespace h264
