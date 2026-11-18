#include "frame_grouping.hpp"

namespace h264inspect {

namespace {

bool picture_had_errors(const PictureInfo& picture) {
    return !picture.uncertainty.empty() || picture.type_conflict ||
           picture.truncated || picture.slices_out_of_order;
}

void copy_picture_fields(FrameInfo& frame, const PictureInfo& picture) {
    frame.type = picture.type;
    frame.is_idr = picture.is_idr;
    frame.had_errors = picture_had_errors(picture);
    frame.reference = picture.reference;
    frame.first_mb_min = picture.min_first_mb;
    frame.first_mb_max = picture.max_first_mb;
    frame.frame_num = picture.frame_num;
    frame.slice_count = picture.slice_count;
    frame.first_nal_offset = picture.first_nal_offset;
    frame.au_start_offset = picture.au_start_offset;
    frame.sps_revision = picture.sps_revision;
    frame.pps_revision = picture.pps_revision;
    frame.uncertain = picture.uncertain;
    frame.uncertainty = picture.uncertainty;
}

}  // namespace

std::vector<FrameInfo> fold_frames(const std::vector<PictureInfo>& pictures) {
    std::vector<FrameInfo> frames;
    frames.reserve(pictures.size());
    for (const PictureInfo& picture : pictures) {
        if (!picture.field_pic) {
            FrameInfo frame;
            copy_picture_fields(frame, picture);
            frame.field_coded = false;
            frames.push_back(frame);
            continue;
        }
        // A complementary field completes the previous, still-isolated field.
        if (!frames.empty()) {
            FrameInfo& previous = frames.back();
            if (previous.field_coded && previous.isolated_field &&
                previous.frame_num == picture.frame_num &&
                previous.is_idr == picture.is_idr &&
                previous.type == picture.type &&
                previous.bottom_field != picture.bottom_field) {
                previous.field_pair = true;
                previous.isolated_field = false;
                previous.slice_count += picture.slice_count;
                previous.had_errors = previous.had_errors ||
                                      picture_had_errors(picture);
                previous.reference = previous.reference || picture.reference;
                previous.uncertain = previous.uncertain || picture.uncertain;
                if (previous.uncertainty.empty()) {
                    previous.uncertainty = picture.uncertainty;
                }
                continue;
            }
        }
        FrameInfo frame;
        copy_picture_fields(frame, picture);
        frame.field_coded = true;
        frame.isolated_field = true;
        frame.bottom_field = picture.bottom_field;
        frames.push_back(frame);
    }
    return frames;
}

std::vector<GopGroup> group_frames(const std::vector<FrameInfo>& frames) {
    std::vector<GopGroup> groups;
    bool have_group = false;
    GopGroup open;
    std::uint32_t previous_sps_revision = 0;
    std::uint32_t previous_pps_revision = 0;
    bool first = true;

    for (std::size_t i = 0; i < frames.size(); ++i) {
        const FrameInfo& frame = frames[i];
        BoundaryReason reason = BoundaryReason::None;
        if (first) {
            reason = BoundaryReason::FirstPicture;
        } else if (frame.is_idr) {
            reason = BoundaryReason::IdrPicture;
        } else if (frame.sps_revision != previous_sps_revision ||
                   frame.pps_revision != previous_pps_revision) {
            reason = BoundaryReason::ParameterSetChange;
        }
        first = false;
        previous_sps_revision = frame.sps_revision;
        previous_pps_revision = frame.pps_revision;

        if (reason != BoundaryReason::None) {
            if (have_group) groups.push_back(open);
            open = GopGroup();
            open.start_nal_offset = frame.au_start_offset;
            open.first_frame_type = frame.type;
            open.reason = reason;
            open.first_frame_index = static_cast<std::uint32_t>(i);
            have_group = true;
        }
        if (!have_group) continue;

        open.slice_count += frame.slice_count;
        open.frame_count += 1;
        if (frame.field_coded) {
            open.field_count += frame.field_pair ? 2u : 1u;
        }
        if (!frame.uncertainty.empty() || frame.uncertain || frame.had_errors) {
            open.uncertain = true;
            if (open.uncertainty.empty()) {
                open.uncertainty =
                    frame.uncertainty.empty() ? "frame contains recoverable errors"
                                              : frame.uncertainty;
            }
        }
    }
    if (have_group) {
        // A group whose final picture was cut short is reported as incomplete.
        open.complete = !open.uncertain;
        if (!open.complete && open.uncertainty.empty()) {
            open.uncertainty = "trailing group contains recoverable errors";
        }
        groups.push_back(open);
    }
    return groups;
}

}  // namespace h264inspect
