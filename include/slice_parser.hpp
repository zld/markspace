// Slice header parsing for group-boundary detection.
#pragma once

#include <cstdint>
#include <string>

#include "bit_reader.hpp"
#include "exp_golomb.hpp"
#include "pps_parser.hpp"
#include "slice_types.hpp"
#include "sps_types.hpp"

namespace h264inspect {



struct SliceHeader {
    std::uint32_t first_mb_in_slice = 0;
    SliceType slice_type = SliceType::Unknown;
    std::uint32_t pps_id = 0;
    std::uint32_t frame_num = 0;
    bool field_pic_flag = false;
    bool bottom_field_flag = false;
    std::uint32_t idr_pic_id = 0;
    std::uint32_t pic_order_cnt_lsb = 0;
    std::int64_t delta_pic_order_cnt_bottom = 0;
    std::int64_t delta_pic_order_cnt_0 = 0;
    std::int64_t delta_pic_order_cnt_1 = 0;

    // Context resolved while parsing.
    std::uint32_t sps_id = 0;
    bool is_idr = false;
    bool nal_ref_idc_zero = false;
    bool have_poc_lsb = false;
    bool have_frame_num = false;

    // False when the payload ended before the last field this analyzer models.
    // The picture identity fields are still trustworthy in that case.
    bool header_complete = true;

    bool is_slice_group_start() const { return first_mb_in_slice == 0; }

    // Identity of the picture this slice belongs to within an AU.
    std::uint32_t picture_key() const {
        return (frame_num << 2) | (field_pic_flag ? (bottom_field_flag ? 2u : 1u) : 0u);
    }
};

struct SliceContext {
    const Sps* sps = nullptr;
    const Pps* pps = nullptr;
};

// Leading fields of a slice header, used to select parameter sets before the
// full header can be parsed.
struct SlicePrefix {
    std::uint32_t first_mb = 0;
    std::uint32_t slice_type_raw = 0;
    std::uint32_t pps_id = 0;
};

// Reads only first_mb_in_slice, slice_type and pic_parameter_set_id.
bool read_slice_prefix(const std::uint8_t* payload, std::size_t payload_size,
                       std::uint64_t payload_offset, ExpGolombStats& stats,
                       SlicePrefix& out);

// Parses a slice header. Requires a resolved SPS and PPS because header layout
// depends on both. On failure, fields decoded so far remain in `out`.
bool parse_slice_header(const std::uint8_t* payload, std::size_t payload_size,
                        std::uint64_t payload_offset, int nal_ref_idc,
                        int nal_unit_type, const SliceContext& context,
                        ExpGolombStats& stats, SliceHeader& out,
                        Diagnostic* diagnostic);

}  // namespace h264inspect
