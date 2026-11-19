#pragma once

#include "diagnostic.hpp"
#include "h264_parser.hpp"
#include "nal_scanner.hpp"

#include <cstddef>
#include <cstdint>
#include <vector>

namespace h264 {

// One decoded picture, possibly assembled from several slice NAL units.
struct ImageRecord {
    // Picture identity fields, taken from the first slice.
    std::uint32_t pic_parameter_set_id = 0;
    std::uint32_t frame_num = 0;
    bool field_pic_flag = false;
    bool bottom_field_flag = false;
    bool idr = false;

    // The picture type follows the first slice; conflicting later slices are
    // recorded as diagnostics instead of silently overwriting it.
    PictureType picture_type = PictureType::kUnknown;

    // First byte of the start code of the first slice NAL of this picture.
    std::uint64_t start_offset_bytes = 0;
    std::vector<std::uint64_t> slice_nal_offsets;
    std::size_t unknown_slice_count = 0;
    bool field_pair_completed = false;
};

struct PictureGroup {
    std::uint64_t start_offset_bytes = 0;
    std::size_t image_count = 0;
    std::size_t i_count = 0;
    std::size_t p_count = 0;
    std::size_t b_count = 0;
};

struct StreamSummary {
    SpsInfo sps;
    std::vector<ImageRecord> images;
    std::vector<PictureGroup> groups;
    std::size_t unknown_slice_count = 0;
    std::size_t unsupported_nal_count = 0;
    DiagnosticSink diagnostics;
};

// Drives scan + parse + access-unit assembly over a whole Annex B buffer.
// Earlier, determinable statistics are retained even when later NAL units are
// truncated or conflict; every such case produces a diagnostic.
StreamSummary analyze_stream(const std::uint8_t* data, std::size_t size);

}  // namespace h264
