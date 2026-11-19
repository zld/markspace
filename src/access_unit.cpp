#include "access_unit.hpp"

namespace h264 {

bool AccessUnitAssembler::same_picture(const PictureInfo& cur,
                                       const SliceHeader& sh) {
    // A continuation slice always carries first_mb_in_slice > 0. The first
    // slice of any picture (including after a frame_num wrap) carries 0, so
    // identity is decided by the picture's key fields plus that condition.
    if (sh.first_mb_in_slice == 0) return false;
    if (cur.is_idr != sh.idr_pic_flag) return false;
    if (cur.frame_num != sh.frame_num) return false;
    if (cur.is_field != sh.field_pic_flag) return false;
    if (sh.field_pic_flag && cur.bottom_field != sh.bottom_field_flag) {
        return false;  // complementary fields are distinct field pictures
    }
    return true;
}

void AccessUnitAssembler::start_gop_for(const PictureInfo& pic) {
    if (!groups_.empty()) {
        groups_.back().closed = true;
    }
    GopInfo g;
    g.start_offset = pic.start_offset;
    g.first_picture = pictures_.size();
    g.picture_count = 0;
    g.closed = false;
    groups_.push_back(g);
}

void AccessUnitAssembler::flush_picture() {
    if (!have_open_) return;

    switch (open_.kind) {
        case FrameKind::I: ++stats_.i; break;
        case FrameKind::P: ++stats_.p; break;
        case FrameKind::B: ++stats_.b; break;
        case FrameKind::Unknown: ++stats_.unknown; break;
    }
    if (open_.is_field) ++stats_.field_pics; else ++stats_.frame_pics;

    if (open_.is_idr) {
        start_gop_for(open_);
    }
    if (groups_.empty()) {
        leading_.push_back(open_);
    } else {
        groups_.back().picture_count++;
    }
    pictures_.push_back(open_);
    have_open_ = false;
}

void AccessUnitAssembler::handle_nal(const NalUnit& nal, Diagnostics& diag) {
    const bool vcl = nal.nal_type >= NAL_SLICE && nal.nal_type <= NAL_IDR;
    if (!vcl) {
        return;  // non-VCL NALs (SPS/PPS/SEI/...) do not open pictures
    }
    if (!nal.valid) {
        diag.add(nal.nal_offset,
                 "skipping structurally invalid VCL NAL of type " +
                     std::to_string(nal.nal_type));
        return;
    }
    if (nal.nal_type == NAL_DPA || nal.nal_type == NAL_DPB ||
        nal.nal_type == NAL_DPC) {
        // Data-partitioned slices (Annex G) are not reconstructed here;
        // record them explicitly rather than guessing a frame type.
        diag.add(nal.nal_offset,
                 "data-partitioned slice NAL (type " +
                     std::to_string(nal.nal_type) +
                     ") is unsupported; counted as unknown picture");
        flush_picture();
        PictureInfo pic;
        pic.start_offset = nal.start_offset;
        pic.kind = FrameKind::Unknown;
        pic.slice_count = 1;
        pic.parse_failed = true;
        open_ = pic;
        have_open_ = true;
        return;
    }

    SliceHeader sh;
    const bool ok = parse_slice_header(nal, store_, sh, diag);

    if (have_open_ && ok && same_picture(open_, sh)) {
        ++open_.slice_count;  // additional slice of the current picture
        return;
    }

    flush_picture();  // previous picture is complete

    PictureInfo pic;
    pic.start_offset = nal.start_offset;
    pic.is_idr = sh.idr_pic_flag;
    pic.is_field = sh.field_pic_flag;
    pic.bottom_field = sh.bottom_field_flag;
    pic.frame_num = sh.frame_num;
    pic.kind = sh.kind;
    pic.pps_id = sh.pps_id;
    pic.slice_count = 1;
    pic.parse_failed = !ok;
    open_ = pic;
    open_header_ = sh;
    have_open_ = true;
}

void AccessUnitAssembler::finish(Diagnostics& diag) {
    (void)diag;
    flush_picture();
    if (!groups_.empty()) {
        // Last group reaches EOF without a terminating IDR: mark unclosed.
        groups_.back().closed = false;
    }
}

}  // namespace h264
