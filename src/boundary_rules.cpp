#include "boundary_rules.hpp"

#include <algorithm>

namespace h264inspect {

BoundaryDecision decide_boundary(const PictureInfo& current, const SliceHeader& slice,
                                 int nal_ref_idc, std::uint32_t sps_revision,
                                 std::uint32_t pps_revision,
                                 bool previous_slice_broken) {
    BoundaryDecision decision;
    if (current.slice_count == 0) {
        decision.new_picture = true;
        return decision;
    }
    if (previous_slice_broken) {
        // An intervening slice could not be parsed, so the picture state may be
        // incomplete. Do not claim a clean boundary without saying so.
        decision.uncertain = true;
        decision.reason =
            "a slice of this picture failed to parse; boundary evidence is partial";
    }
    if (slice.first_mb_in_slice != 0) {
        // Never split a picture on a non-initial macroblock: out-of-order (ASO)
        // slice delivery must stay in the current picture.
        if (slice.frame_num != current.frame_num ||
            slice.pps_id != current.pps_id) {
            decision.uncertain = true;
            if (decision.reason.empty()) {
                decision.reason =
                    "slice with first_mb_in_slice != 0 disagrees with the current "
                    "picture";
            }
        }
        return decision;
    }

    auto set = [&](const char* why) {
        decision.new_picture = true;
        decision.reason = why;
    };
    if (slice.is_idr != current.is_idr) {
        set("IDR/non-IDR coding changed");
    } else if (slice.is_idr && slice.idr_pic_id != current.idr_pic_id) {
        set("idr_pic_id changed");
    } else if (slice.pps_id != current.pps_id) {
        set("pic_parameter_set_id changed");
    } else if (sps_revision != current.sps_revision) {
        set("activated SPS content changed");
    } else if (pps_revision != current.pps_revision) {
        set("activated PPS content changed");
    } else if (slice.field_pic_flag != current.field_pic) {
        set("field_pic_flag changed");
    } else if (slice.field_pic_flag &&
               slice.bottom_field_flag != current.bottom_field) {
        set("bottom_field_flag changed");
    } else if (slice.frame_num != current.frame_num) {
        set("frame_num changed");
    } else if ((nal_ref_idc == 0) != !current.reference) {
        set("nal_ref_idc changed between reference and non-reference");
    } else if (slice.have_poc_lsb && current.have_poc_lsb &&
               slice.pic_order_cnt_lsb != current.pic_order_cnt_lsb) {
        set("pic_order_cnt_lsb changed");
    } else if (slice.delta_pic_order_cnt_bottom !=
                   current.delta_pic_order_cnt_bottom &&
               !current.field_pic) {
        set("delta_pic_order_cnt_bottom changed");
    }
    return decision;
}

void apply_slice(PictureInfo& picture, const SliceHeader& slice, int nal_ref_idc,
                 std::uint32_t sps_id, std::uint32_t sps_revision,
                 std::uint32_t pps_revision, std::uint64_t nal_offset) {
    if (picture.slice_count == 0) {
        picture.type = slice.slice_type;
        picture.is_idr = slice.is_idr;
        picture.field_pic = slice.field_pic_flag;
        picture.bottom_field = slice.bottom_field_flag;
        picture.idr_pic_id = slice.idr_pic_id;
        picture.reference = nal_ref_idc != 0;
        picture.reference_consistent = true;
        picture.frame_num = slice.frame_num;
        picture.pic_order_cnt_lsb = slice.pic_order_cnt_lsb;
        picture.have_poc_lsb = slice.have_poc_lsb;
        picture.delta_pic_order_cnt_bottom = slice.delta_pic_order_cnt_bottom;
        picture.pps_id = slice.pps_id;
        picture.sps_id = sps_id;
        picture.sps_revision = sps_revision;
        picture.pps_revision = pps_revision;
        picture.first_nal_offset = nal_offset;
        picture.min_first_mb = slice.first_mb_in_slice;
        picture.max_first_mb = slice.first_mb_in_slice;
        picture.slices_out_of_order = false;
    } else {
        if (slice.slice_type != picture.type) picture.type_conflict = true;
        if ((nal_ref_idc != 0) != picture.reference) {
            // nal_ref_idc must not vary inside one primary coded picture.
            picture.reference_consistent = false;
        }
        picture.reference = picture.reference || nal_ref_idc != 0;
        if (slice.first_mb_in_slice < picture.max_first_mb) {
            picture.slices_out_of_order = true;
        }
        picture.min_first_mb = std::min(picture.min_first_mb,
                                        slice.first_mb_in_slice);
        picture.max_first_mb = std::max(picture.max_first_mb,
                                        slice.first_mb_in_slice);
    }
    ++picture.slice_count;
}

PictureInfo degraded_picture(std::uint32_t slice_type_raw, bool is_idr,
                             int nal_ref_idc, std::uint32_t pps_id,
                             std::uint32_t sps_id, std::uint32_t sps_revision,
                             std::uint32_t pps_revision, std::uint32_t first_mb,
                             std::uint64_t nal_offset, std::uint64_t au_start_offset,
                             const std::string& note) {
    PictureInfo picture;
    picture.type = slice_type_raw <= 9
                       ? static_cast<SliceType>(slice_type_raw % 5)
                       : SliceType::Unknown;
    picture.is_idr = is_idr;
    picture.reference = nal_ref_idc != 0;
    picture.reference_consistent = true;
    picture.pps_id = pps_id;
    picture.sps_id = sps_id;
    picture.sps_revision = sps_revision;
    picture.pps_revision = pps_revision;
    picture.first_nal_offset = nal_offset;
    picture.au_start_offset = au_start_offset;
    picture.slice_count = 1;
    picture.min_first_mb = first_mb;
    picture.max_first_mb = first_mb;
    picture.truncated = true;
    picture.uncertain = true;
    picture.uncertainty = note;
    return picture;
}

}  // namespace h264inspect
