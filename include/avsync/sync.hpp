#pragma once

#include <cstdint>
#include <optional>

extern "C" {
#include <libavutil/rational.h>
}

namespace avsync {

// Normalizes raw container timestamps onto a single zero-based timeline.
// Handles non-zero start times, missing PTS (fall back to DTS + frame
// duration) and timestamp regressions (rebuild the baseline).
class TimestampNormalizer {
 public:
  explicit TimestampNormalizer(AVRational time_base);

  // Returns normalized presentation time in seconds, or nullopt if neither
  // PTS nor DTS is available. |frame_duration_seconds| is used when PTS is
  // missing and as the assumed spacing for monotonic repair.
  std::optional<double> normalize(int64_t pts, int64_t dts,
                                  double frame_duration_seconds);

 private:
  AVRational time_base_;
  bool have_origin_ = false;
  int64_t origin_ts_ = 0;   // Raw timestamp mapped to normalized time zero.
  double last_output_ = 0.0;
  int64_t sequence_ = 0;    // Count of emitted timestamps.
};

// What the video scheduler should do with the head frame right now.
enum class SyncAction {
  Show,       // Present immediately.
  Wait,       // Too early: sleep and re-evaluate.
  Drop,       // Too late beyond the timeout: skip and count.
  Repeat,     // No frame ready at its slot: re-present the previous frame.
};

struct SyncPolicy {
  double frame_duration_seconds = 1.0 / 30.0;
  double early_threshold_seconds = 0.005;   // Below this: show anyway.
  double drop_timeout_seconds = 0.100;      // Past due beyond this: drop.
  double repeat_gap_seconds = 0.005;        // Empty slot longer than this: repeat.
  double max_wait_seconds = 0.050;          // Cap a single sleep.
};

// Shared decision logic for both output paths; audio consumes at its own rate
// and queries the same clock, while video frames are scheduled here.
class Synchronizer {
 public:
  explicit Synchronizer(SyncPolicy policy = {});

  // |frame_pts| is normalized presentation time, |clock_seconds| the master
  // clock reading. Wait time is returned via |out_wait_seconds|.
  SyncAction decide(double frame_pts, double clock_seconds,
                    double* out_wait_seconds) const;

  // Called when no frame is available at the next slot. Returns Repeat when
  // the gap since |last_shown_pts| warrants re-presenting the previous frame.
  bool should_repeat(double clock_seconds,
                     std::optional<double> last_shown_pts) const;

  const SyncPolicy& policy() const { return policy_; }

 private:
  SyncPolicy policy_;
};

}  // namespace avsync
