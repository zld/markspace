#include "avsync/sync.hpp"

#include <algorithm>

extern "C" {
#include <libavutil/avutil.h>
#include <libavutil/mathematics.h>
}

namespace avsync {

TimestampNormalizer::TimestampNormalizer(AVRational time_base)
    : time_base_(time_base) {}

std::optional<double> TimestampNormalizer::normalize(
    int64_t pts, int64_t dts, double frame_duration_seconds) {
  const bool have_pts = pts != AV_NOPTS_VALUE;
  const bool have_dts = dts != AV_NOPTS_VALUE;
  if (!have_pts && !have_dts) return std::nullopt;

  int64_t raw = have_pts ? pts : dts;
  if (!have_origin_) {
    // First valid timestamp anchors the zero-based timeline, absorbing any
    // non-zero container start time.
    origin_ts_ = raw;
    have_origin_ = true;
  } else if (raw < origin_ts_) {
    // Timestamp regression: rebuild the baseline while keeping output
    // monotonic by continuing from the last emitted position.
    origin_ts_ = raw;
  }

  double seconds =
      static_cast<double>(raw - origin_ts_) * av_q2d(time_base_);
  if (!have_pts) {
    // PTS missing: derive it from DTS plus one frame duration.
    seconds += frame_duration_seconds;
  }

  // Defensive monotonic repair for equal/out-of-order values.
  if (sequence_ > 0 && seconds < last_output_ + 1e-6) {
    seconds = last_output_ + frame_duration_seconds;
  }
  last_output_ = seconds;
  ++sequence_;
  return std::max(0.0, seconds);
}

Synchronizer::Synchronizer(SyncPolicy policy) : policy_(policy) {}

SyncAction Synchronizer::decide(double frame_pts, double clock_seconds,
                                double* out_wait_seconds) const {
  const double delta = frame_pts - clock_seconds;
  if (out_wait_seconds) *out_wait_seconds = 0.0;

  if (delta > policy_.early_threshold_seconds) {
    if (out_wait_seconds) {
      *out_wait_seconds =
          std::min(delta - policy_.early_threshold_seconds,
                   policy_.max_wait_seconds);
    }
    return SyncAction::Wait;
  }
  if (delta < -policy_.drop_timeout_seconds) {
    return SyncAction::Drop;
  }
  return SyncAction::Show;
}

bool Synchronizer::should_repeat(
    double clock_seconds, std::optional<double> last_shown_pts) const {
  if (!last_shown_pts.has_value()) return false;
  return clock_seconds - *last_shown_pts >=
         policy_.frame_duration_seconds + policy_.repeat_gap_seconds;
}

}  // namespace avsync
