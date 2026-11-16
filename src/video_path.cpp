#include "avsync/video_path.hpp"

#include <algorithm>

namespace avsync {

VideoPath::VideoPath(FrameQueue& frames, const Synchronizer& synchronizer,
                     Metrics& metrics, JitterSource& jitter)
    : frames_(frames),
      sync_(synchronizer),
      metrics_(metrics),
      jitter_(jitter) {}

void VideoPath::record_delta(double frame_pts, double clock_seconds) {
  // Signed delta: positive means video is early relative to the audio clock.
  metrics_.record_av_delta((frame_pts - clock_seconds) * 1000.0);
}

bool VideoPath::update(double clock_seconds, bool& fatal_error) {
  fatal_error = false;

  // Fill empty presentation slots by repeating the previous frame.
  if (last_tick_clock_ >= 0.0 && last_shown_pts_.has_value() &&
      sync_.should_repeat(clock_seconds, last_shown_pts_)) {
    metrics_.add_video_repeat();
    ++repeated_;
  }
  last_tick_clock_ = clock_seconds;

  if (!head_.has_value() && !eof_) {
    head_ = frames_.try_pop();
    if (!head_.has_value()) {
      if (frames_.error()) {
        fatal_error = true;
        return false;
      }
      if (frames_.drained()) eof_ = true;
      return true;  // Nothing to schedule yet; next tick may repeat.
    }
  }

  while (head_.has_value()) {
    double wait_seconds = 0.0;
    const SyncAction action =
        sync_.decide(head_->pts_seconds, clock_seconds, &wait_seconds);

    switch (action) {
      case SyncAction::Wait:
        // Inject deterministic jitter, then sleep bounded by the policy.
        sleep_ns(std::min(seconds_to_ns(wait_seconds),
                          seconds_to_ns(sync_.policy().max_wait_seconds)) +
                 jitter_.next_delay_us() * 1000);
        return true;  // Re-evaluate on the next tick.

      case SyncAction::Drop:
        record_delta(head_->pts_seconds, clock_seconds);
        metrics_.add_video_drop();
        ++dropped_;
        head_ = frames_.try_pop();
        if (!head_.has_value() && frames_.drained()) eof_ = true;
        break;

      case SyncAction::Show:
        record_delta(head_->pts_seconds, clock_seconds);
        metrics_.add_video_frame();
        ++shown_;
        last_shown_pts_ = head_->pts_seconds;
        head_ = frames_.try_pop();
        if (!head_.has_value()) {
          if (frames_.error()) {
            fatal_error = true;
            return false;
          }
          if (frames_.drained()) eof_ = true;
        }
        return true;

      case SyncAction::Repeat:
        // Decided only via should_repeat(); defensively re-show and continue.
        metrics_.add_video_repeat();
        ++repeated_;
        return true;
    }
  }
  return true;
}

bool VideoPath::finished() const {
  return eof_ && !head_.has_value();
}

}  // namespace avsync
