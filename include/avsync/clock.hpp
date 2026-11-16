#pragma once

#include <atomic>
#include <cstdint>
#include <mutex>

namespace avsync {

// Master clock driven by consumed audio samples. While no audio track exists,
// or while the audio path reports an underrun, the monotonic wall clock takes
// over. All drift corrections are clamped so a single bad measurement cannot
// jump the timeline.
class MasterClock {
 public:
  // |audio_sample_rate| <= 0 selects wall-clock-only mode.
  explicit MasterClock(int audio_sample_rate);

  // Reset and start the clock. |realtime_factor| scales wall-clock progression
  // (2.0 runs the simulated timeline twice as fast as real life).
  void start(double realtime_factor);

  // Advance media time by |samples| consumed from the resampled audio queue.
  void consume_audio(int samples);

  // Audio path ran dry: rebase the wall-clock follower onto media time so the
  // fallback does not introduce a discontinuity.
  void notify_audio_underrun();

  // Current media position in seconds from the (possibly shifted) origin.
  double now_seconds() const;
  // Unscaled wall-clock reading, for diagnostics and wait decisions.
  double wall_seconds() const;

  bool has_audio() const { return audio_rate_ > 0; }
  // Last measured wall-vs-media residual, milliseconds (positive = media late).
  double drift_ms() const;

 private:
  double wall_elapsed_seconds_locked() const;
  double media_seconds_locked() const;

  mutable std::mutex mutex_;
  int audio_rate_;
  double realtime_factor_ = 1.0;

  int64_t start_wall_ns_ = 0;
  int64_t consumed_samples_ = 0;

  // Offset applied to the wall follower so it stays continuous across
  // audio/mono handoffs. In media-seconds.
  double mono_offset_ = 0.0;
  int64_t last_rebase_wall_ns_ = 0;

  double last_drift_ms_ = 0.0;
  std::atomic<bool> running_{false};

  static constexpr double kMaxDriftStepMs = 50.0;
};

}  // namespace avsync
