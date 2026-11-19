#include "h264_parser.hpp"

#include "bit_reader.hpp"

#include <string>

namespace h264 {
namespace {

PictureType classify_slice(std::uint32_t slice_type) {
    if (slice_type == 0 || slice_type == 5) return PictureType::kP;
    if (slice_type == 1 || slice_type == 6) return PictureType::kB;
    if (slice_type == 2 || slice_type == 7) return PictureType::kI;
    return PictureType::kUnknown;
}

// Bounds-checked payload view with emulation prevention bytes removed.
std::vector<std::uint8_t> payload_rbsp_slice(const NalUnit& nal,
                                             const std::uint8_t* stream) {
    return rbsp_unescape(stream + nal.payload_offset, nal.payload_size);
}

}  // namespace

const char* picture_type_name(PictureType type) {
    switch (type) {
        case PictureType::kI: return "I";
        case PictureType::kP: return "P";
        case PictureType::kB: return "B";
        default: return "unknown";
    }
}

SliceInfo parse_slice_header(const NalUnit& nal, std::size_t nal_index,
                             const std::uint8_t* stream, const SpsInfo* sps,
                             DiagnosticSink& diag) {
    SliceInfo slice;
    slice.nal_index = nal_index;
    slice.nal_offset = nal.start_offset;
    slice.idr = nal.unit_type == kNalIdrSlice;

    auto rbsp = payload_rbsp_slice(nal, stream);
    BitReader br(rbsp.data(), rbsp.size());

    if (!br.read_ue(slice.first_mb_in_slice) ||
        !br.read_ue(slice.slice_type) ||
        !br.read_ue(slice.pic_parameter_set_id)) {
        diag.error(nal.start_offset, "slice_truncated",
                   "slice header ended before first_mb/type/pps id");
        return slice;
    }
    slice.picture_type = classify_slice(slice.slice_type);
    if (slice.picture_type == PictureType::kUnknown) {
        diag.warning(nal.start_offset, "unknown_slice_type",
                     "slice_type " + std::to_string(slice.slice_type) +
                         " is outside the I/P/B range (0..7)");
    }

    if (sps == nullptr || !sps->present || !sps->valid) {
        diag.error(nal.start_offset, "missing_parameter_set",
                   "no valid SPS available; frame_num read with default width");
    }
    const std::uint32_t frame_num_bits =
        4u + (sps != nullptr && sps->present ? sps->log2_max_frame_num_minus4
                                            : 0u);
    if (!br.read_bits(frame_num_bits, slice.frame_num)) {
        diag.error(nal.start_offset, "slice_truncated",
                   "slice header ended before frame_num");
        return slice;
    }

    if (sps != nullptr && sps->present && !sps->frame_mbs_only_flag) {
        uint32_t field_pic = 0;
        if (!br.read_bits(1, field_pic)) {
            diag.error(nal.start_offset, "slice_truncated",
                       "slice header ended before field_pic_flag");
            return slice;
        }
        slice.field_pic_flag = field_pic != 0;
        if (slice.field_pic_flag) {
            uint32_t bottom = 0;
            if (!br.read_bits(1, bottom)) {
                diag.error(nal.start_offset, "slice_truncated",
                           "field slice header ended before bottom_field_flag");
                return slice;
            }
            slice.bottom_field_flag = bottom != 0;
        }
    }
    if (slice.idr) {
        uint32_t idr_pic_id = 0;
        if (!br.read_ue(idr_pic_id)) {
            diag.error(nal.start_offset, "slice_truncated",
                       "IDR slice header ended before idr_pic_id");
            return slice;
        }
    }

    slice.valid = true;
    return slice;
}

}  // namespace h264
