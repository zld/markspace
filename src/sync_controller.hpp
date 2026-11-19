#pragma once

#include <cstdint>

namespace avsync {

enum class FrameAction { kWait, kShow, kDrop, kRepeat };

struct SyncDecision {
    FrameAction action = FrameAction::kShow;
    int64_t wait_us = 0;         // suggested wait before showing (kWait)
    int64_t offset_us = 0;       // effective_pts - clock at decision time
    int64_t effective_pts_us = 0;  // pts actually used (estimated if missing)
    bool pts_estimated = false;  // true when pts was missing/abnormal
};

// Decides what the video path does with each frame, given its presentation
// timestamp and the shared master clock. All math is integer microseconds.
class SyncController {
public:
    // early_threshold_us: show immediately when the frame is at most this
    //   early; otherwise wait.
    // drop_threshold_us: drop frames later than this behind the clock.
    // frame_duration_us: nominal frame slot, used for pts estimation.
    SyncController(int64_t early_threshold_us, int64_t drop_threshold_us,
                   int64_t frame_duration_us);

    // pts_valid=false (or a non-monotonic / absurd pts) triggers estimation
    // from the last shown frame. Never blocks, never throws.
    SyncDecision decide(int64_t pts_us, bool pts_valid, int64_t clock_us);

    void on_frame_shown(int64_t pts_us);

    int64_t frame_duration_us() const { return frame_dur_us_; }
    int64_t last_shown_pts_us() const { return last_pts_us_; }
    bool has_last() const { return have_last_; }

    // Hard cap on any suggested wait so absurd future timestamps cannot
    // stall the pipeline.
    static constexpr int64_t kMaxWaitUs = 1000000;

private:
    int64_t early_us_;
    int64_t drop_us_;
    int64_t frame_dur_us_;
    int64_t last_pts_us_ = 0;
    bool have_last_ = false;
};

}  // namespace avsync
