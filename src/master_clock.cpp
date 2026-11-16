#include "avsync/clock.hpp"

#include <algorithm>

#include "avsync/common.hpp"

namespace avsync {

MasterClock::MasterClock(int audio_sample_rate)
    : audio_rate_(audio_sample_rate) {}

void MasterClock::start(double realtime_factor) {
  std::lock_guard<std::mutex> lock(mutex_);
  realtime_factor_ = realtime_factor > 0.0 ? realtime_factor : 1.0;
  start_wall_ns_ = now_ns();
  last_rebase_wall_ns_ = start_wall_ns_;
  consumed_samples_ = 0;
  mono_offset_ = 0.0;
  last_drift_ms_ = 0.0;
  running_.store(true);
}

double MasterClock::wall_elapsed_seconds_locked() const {
  return static_cast<double>(now_ns() - start_wall_ns_) / 1.0e9;
}

double MasterClock::media_seconds_locked() const {
  if (audio_rate_ > 0) {
    return static_cast<double>(consumed_samples_) /
           static_cast<double>(audio_rate_);
  }
  const double scaled_wall =
      (static_cast<double>(now_ns() - last_rebase_wall_ns_) / 1.0e9) *
          realtime_factor_ +
      mono_offset_;
  return std::max(0.0, scaled_wall);
}

void MasterClock::consume_audio(int samples) {
  if (audio_rate_ <= 0 || samples <= 0) return;
  std::lock_guard<std::mutex> lock(mutex_);
  if (!running_.load()) return;

  const double before = media_seconds_locked();
  consumed_samples_ += samples;
  const double media = media_seconds_locked();

  // Compare against the unscaled wall clock: drift measures how far the
  // sample-driven media position has drifted from real elapsed time.
  const double wall = wall_elapsed_seconds_locked();
  double drift_ms = (media - wall) * 1000.0;
  drift_ms = std::clamp(drift_ms, -kMaxDriftStepMs, kMaxDriftStepMs);
  last_drift_ms_ = drift_ms;
  (void)before;
}

void MasterClock::notify_audio_underrun() {
  if (audio_rate_ <= 0) return;
  std::lock_guard<std::mutex> lock(mutex_);
  if (!running_.load()) return;
  // Hand the current media position to the wall-clock follower. The follower
  // continues from |mono_offset_| scaled by realtime_factor; clamp the handoff
  // so a single stall cannot translate into a large timeline jump.
  const double media = media_seconds_locked();
  mono_offset_ =
      std::clamp(media, mono_offset_ - kMaxDriftStepMs / 1000.0,
                 mono_offset_ + kMaxDriftStepMs / 1000.0);
  last_rebase_wall_ns_ = now_ns();
}

double MasterClock::now_seconds() const {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!running_.load()) return 0.0;
  return media_seconds_locked();
}

double MasterClock::wall_seconds() const {
  std::lock_guard<std::mutex> lock(mutex_);
  if (!running_.load()) return 0.0;
  return wall_elapsed_seconds_locked();
}

double MasterClock::drift_ms() const {
  std::lock_guard<std::mutex> lock(mutex_);
  return last_drift_ms_;
}

}  // namespace avsync
