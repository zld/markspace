#ifndef H264_SLICE_PARSER_HPP
#define H264_SLICE_PARSER_HPP

#include <cstdint>

#include "diagnostics.hpp"
#include "nal_parser.hpp"
#include "parameter_sets.hpp"

namespace h264 {

// H.264 Table 7-6 slice types, including the >4 "all slices" aliases.
enum SliceType : unsigned {
    SLICE_P = 0,
    SLICE_B = 1,
    SLICE_I = 2,
    SLICE_SP = 3,
    SLICE_SI = 4,
};

enum class FrameKind {
    I, P, B, Unknown,
};

const char* frame_kind_name(FrameKind k);

// Subset of slice header (7.3.3) needed to group slices into pictures and
// classify frame types. Fields are left at defaults when parsing fails, but
// `parse_ok` records the failure so the caller can keep the error location.
struct SliceHeader {
    unsigned first_mb_in_slice = 0;
    unsigned slice_type_raw = 0;      // value as encoded, 0..9
    unsigned pps_id = 0;
    unsigned frame_num = 0;
    bool idr_pic_flag = false;
    unsigned idr_pic_id = 0;
    bool field_pic_flag = false;
    bool bottom_field_flag = false;   // only meaningful when field_pic_flag
    bool parse_ok = false;
    FrameKind kind = FrameKind::Unknown;

    // Normalized 0..4 type; >4 aliases map back to their base type.
    unsigned base_slice_type() const noexcept {
        return slice_type_raw >= 5 ? slice_type_raw - 5 : slice_type_raw;
    }
};

// Parses a VCL NAL (types 1..5). Needs the parameter set store to resolve
// frame_num width and field-coding flags via PPS -> SPS. A missing PPS/SPS is
// recorded as a diagnostic (with the NAL offset) and yields parse_ok=false;
// the scanner can safely continue with later NALs.
bool parse_slice_header(const NalUnit& nal,
                        const ParameterSetStore& store,
                        SliceHeader& out,
                        Diagnostics& diag);

}  // namespace h264

#endif
