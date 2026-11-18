// Access-unit boundary rules and picture accumulation.
#pragma once

#include <cstdint>
#include <string>

#include "analysis_result.hpp"
#include "slice_parser.hpp"

namespace h264inspect {

struct BoundaryDecision {
    bool new_picture = false;
    bool uncertain = false;
    std::string reason;
};

// Applies the first_mb_in_slice == 0 rules of H.264 7.4.1.2.4 for the fields
// this analyzer tracks. `previous_slice_broken` records that an intervening
// slice could not be parsed, which weakens the evidence for a clean split.
BoundaryDecision decide_boundary(const PictureInfo& current, const SliceHeader& slice,
                                 int nal_ref_idc, std::uint32_t sps_revision,
                                 std::uint32_t pps_revision,
                                 bool previous_slice_broken);

// Folds one slice into a picture record, first slice or additional slice.
void apply_slice(PictureInfo& picture, const SliceHeader& slice, int nal_ref_idc,
                 std::uint32_t sps_id, std::uint32_t sps_revision,
                 std::uint32_t pps_revision, std::uint64_t nal_offset);

// Creates a picture record from only the peeked slice prefix and NAL header,
// used when the full header cannot be parsed but the picture is identifiable.
PictureInfo degraded_picture(std::uint32_t slice_type_raw, bool is_idr,
                             int nal_ref_idc, std::uint32_t pps_id,
                             std::uint32_t sps_id, std::uint32_t sps_revision,
                             std::uint32_t pps_revision, std::uint32_t first_mb,
                             std::uint64_t nal_offset, std::uint64_t au_start_offset,
                             const std::string& note);

}  // namespace h264inspect
