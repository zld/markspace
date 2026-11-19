#pragma once

#include "nal_scanner.hpp"

#include <cstdint>
#include <vector>

namespace h264 {

enum class PictureType : std::uint8_t {
    kUnknown = 0,
    kI = 1,
    kP = 2,
    kB = 3,
};

const char* picture_type_name(PictureType type);

// Parsed SPS fields this tool understands.  `valid` is false when the SPS was
// truncated or carried an unsupported extension.
struct SpsInfo {
    bool present = false;
    bool valid = false;
    std::uint32_t profile_idc = 0;
    std::uint32_t level_idc = 0;
    std::uint32_t seq_parameter_set_id = 0;
    std::uint32_t width = 0;
    std::uint32_t height = 0;
    std::uint32_t log2_max_frame_num_minus4 = 0;
    bool frame_mbs_only_flag = true;
    // Cropping offsets in frame samples; all zero when cropping is absent.
    std::uint32_t crop_left = 0;
    std::uint32_t crop_right = 0;
    std::uint32_t crop_top = 0;
    std::uint32_t crop_bottom = 0;
};

struct PpsInfo {
    bool present = false;
    std::uint32_t pic_parameter_set_id = 0;
    std::uint32_t seq_parameter_set_id = 0;
};

// A parsed slice header.  `valid` means picture identity fields were read;
// picture_type stays kUnknown when slice_type itself is out of range.
struct SliceInfo {
    bool valid = false;
    std::size_t nal_index = 0;
    std::uint64_t nal_offset = 0;

    std::uint32_t first_mb_in_slice = 0;
    std::uint32_t slice_type = 0;
    PictureType picture_type = PictureType::kUnknown;
    std::uint32_t pic_parameter_set_id = 0;
    std::uint32_t frame_num = 0;
    bool field_pic_flag = false;
    bool bottom_field_flag = false;
    bool idr = false;
};

// Parses one SPS NAL unit.  Errors and unsupported structures are appended to
// the sink with byte offsets relative to the input file.
SpsInfo parse_sps(const NalUnit& nal, const std::uint8_t* stream,
                  DiagnosticSink& diag);

// Parses only the PPS identity fields needed to bind a PPS to its SPS.
PpsInfo parse_pps(const NalUnit& nal, const std::uint8_t* stream,
                  DiagnosticSink& diag);

// Parses a slice header.  `sps` may be absent (nullptr / invalid); in that
// case a diagnostic is emitted and frame_num falls back to 4 bits so the
// identity fields can still be recovered deterministically.
SliceInfo parse_slice_header(const NalUnit& nal, std::size_t nal_index,
                             const std::uint8_t* stream, const SpsInfo* sps,
                             DiagnosticSink& diag);

}  // namespace h264
