#include "sync_controller.hpp"

namespace avsync {

SyncController::SyncController(int64_t early_threshold_us,
                               int64_t drop_threshold_us,
                               int64_t frame_duration_us)
    : early_us_(early_threshold_us),
      drop_us_(drop_threshold_us),
      frame_dur_us_(frame_duration_us > 0 ? frame_duration_us : 1) {}

SyncDecision SyncController::decide(int64_t pts_us, bool pts_valid,
                                    int64_t clock_us) {
    SyncDecision d;
    int64_t pts = pts_us;
    // Reject missing, non-monotonic, or absurd timestamps and estimate
    // from the last shown frame instead.
    bool absurd = pts_us > INT64_MAX / 2 || pts_us < INT64_MIN / 2;
    bool nonmono = pts_valid && have_last_ && pts_us <= last_pts_us_;
    if (!pts_valid || absurd || nonmono) {
        pts = have_last_ ? last_pts_us_ + frame_dur_us_ : clock_us;
        d.pts_estimated = true;
    }
    d.effective_pts_us = pts;
    // Guard against overflow in pts - clock.
    int64_t offset;
    if (pts > clock_us && pts - clock_us > 0) {
        offset = pts - clock_us;
    } else if (pts < clock_us && pts - clock_us < 0) {
        offset = pts - clock_us;
    } else {
        offset = 0;
    }
    d.offset_us = offset;

    if (offset > early_us_) {
        d.action = FrameAction::kWait;
        d.wait_us = offset > kMaxWaitUs ? kMaxWaitUs : offset;
    } else if (offset < -drop_us_) {
        d.action = FrameAction::kDrop;
    } else {
        d.action = FrameAction::kShow;
    }
    return d;
}

void SyncController::on_frame_shown(int64_t pts_us) {
    last_pts_us_ = pts_us;
    have_last_ = true;
}

}  // namespace avsync
