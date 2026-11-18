// Result types produced by stream analysis.
#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "byte_source.hpp"
#include "diagnostics.hpp"
#include "exp_golomb.hpp"
#include "nal_parser.hpp"
#include "pps_parser.hpp"
#include "slice_parser.hpp"
#include "sps_parser.hpp"

namespace h264inspect {

enum class BoundaryReason {
    None = 0,
    FirstPicture = 1,        // first coded picture in the stream
    IdrPicture = 2,          // IDR sweep starts a new decoding sequence
    ParameterSetChange = 3,  // activated SPS/PPS identity or content changed
};

const char* boundary_reason_name(BoundaryReason reason);

// One coded picture (all slices sharing frame_num, PPS, field parity, ...).
struct PictureInfo {
    SliceType type = SliceType::Unknown;
    bool type_conflict = false;   // slices of one picture disagree on slice type
    bool is_idr = false;
    bool field_pic = false;
    bool bottom_field = false;
    std::uint32_t idr_pic_id = 0;
    bool reference = false;       // nal_ref_idc != 0 for any slice
    bool reference_consistent = true;  // all slices agree on reference status
    std::uint32_t frame_num = 0;
    std::uint32_t pic_order_cnt_lsb = 0;
    bool have_poc_lsb = false;
    std::int64_t delta_pic_order_cnt_bottom = 0;
    std::uint32_t pps_id = 0;
    std::uint32_t sps_id = 0;
    std::uint32_t sps_revision = 0;
    std::uint32_t pps_revision = 0;
    std::uint32_t slice_count = 0;
    std::uint32_t min_first_mb = 0;
    std::uint32_t max_first_mb = 0;
    std::uint64_t first_nal_offset = 0;  // start code of the first slice
    std::uint64_t au_start_offset = 0;   // first NAL of the access unit
    bool slices_out_of_order = false;
    bool truncated = false;   // a slice of this picture failed to parse
    bool uncertain = false;
    std::string uncertainty;
};

struct FrameInfo {
    SliceType type = SliceType::Unknown;
    bool is_idr = false;
    bool field_coded = false;     // pictures were coded as fields
    bool field_pair = false;      // top+bottom fields merged into one frame
    bool isolated_field = false;  // field without a complementary field
    bool bottom_field = false;    // parity of an isolated field
    bool had_errors = false;
    bool reference = false;
    std::uint32_t first_mb_min = 0;   // lowest first_mb_in_slice in the frame
    std::uint32_t first_mb_max = 0;   // highest first_mb_in_slice in the frame
    std::uint32_t frame_num = 0;
    std::uint32_t slice_count = 0;
    std::uint64_t first_nal_offset = 0;
    std::uint64_t au_start_offset = 0;
    std::uint32_t sps_revision = 0;
    std::uint32_t pps_revision = 0;
    bool uncertain = false;
    std::string uncertainty;
};

struct GopGroup {
    std::uint64_t start_nal_offset = 0;  // first NAL byte of the group
    SliceType first_frame_type = SliceType::Unknown;
    BoundaryReason reason = BoundaryReason::None;
    std::uint32_t slice_count = 0;
    std::uint32_t frame_count = 0;
    std::uint32_t field_count = 0;
    std::uint32_t first_frame_index = 0;
    bool complete = true;
    bool uncertain = false;
    std::string uncertainty;
};

struct AnalysisResult {
    std::vector<Sps> sps_list;  // every successfully parsed SPS occurrence
    std::vector<Pps> pps_list;
    std::vector<Diagnostic> errors;
    NalScanSummary nal_summary;
    ExpGolombStats exp_golomb;
    std::vector<PictureInfo> pictures;
    std::vector<FrameInfo> frames;
    std::vector<GopGroup> groups;
    std::uint32_t nal_count = 0;

    bool has_recoverable_errors() const { return !errors.empty(); }

    // True when a picture or group could not be placed with confidence.
    bool has_uncertain_boundaries() const {
        for (const PictureInfo& picture : pictures) {
            if (picture.uncertain) return true;
        }
        for (const GopGroup& group : groups) {
            if (group.uncertain) return true;
        }
        return false;
    }
};

}  // namespace h264inspect
