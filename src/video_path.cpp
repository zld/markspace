#include "avsync/video_path.hpp"

#include <algorithm>

namespace avsync {

VideoPath::VideoPath(FrameQueue& frames, const Synchronizer& synchronizer,
                     Metrics& metrics)
    : frames_(frames), sync_(synchronizer), metrics_(metrics) {}

void VideoPath::record_delta(double frame_pts, double clock_seconds) {
  // Signed delta: positive means video is early relative to the audio clock.
  metrics_.record_av_delta((frame_pts - clock_seconds) * 1000.0);
}

bool VideoPath::update(double clock_seconds, bool& fatal_error) {
  fatal_error = false;

  if (!head_.has_value() && !eof_) {
    head_ = frames_.try_pop();
    if (!head_.has_value()) {
      if (frames_.error()) {
        fatal_error = true;
        return false;
      }
      if (frames_.drained()) eof_ = true;
    }
  }

  bool showed_or_dropped = false;

  // Drain every frame whose presentation slot is already due, without ever
  // blocking this scheduler thread: early frames stay queued for later ticks.
  while (head_.has_value()) {
    double wait_seconds = 0.0;
    const SyncAction action =
        sync_.decide(head_->pts_seconds, clock_seconds, &wait_seconds);

    if (action == SyncAction::Wait) {
      break;  // Too early; re-evaluate on the next tick.
    }

    record_delta(head_->pts_seconds, clock_seconds);
    if (action == SyncAction::Drop) {
      metrics_.add_video_drop();
      ++dropped_;
    } else {
      metrics_.add_video_frame();
      ++shown_;
      last_shown_pts_ = head_->pts_seconds;
    }
    showed_or_dropped = true;

    head_ = frames_.try_pop();
    if (!head_.has_value()) {
      if (frames_.error()) {
        fatal_error = true;
        return false;
      }
      if (frames_.drained()) eof_ = true;
    }
  }

  // A repeat is only meaningful when this tick presented nothing new yet a
  // whole frame slot has elapsed since the last shown frame. Counting it here
  // (rather than unconditionally at tick entry) avoids double-counting on ticks
  // that also show a frame, and bounds repeats to one per tick.
  if (!showed_or_dropped &&
      sync_.should_repeat(clock_seconds, last_shown_pts_)) {
    metrics_.add_video_repeat();
    ++repeated_;
  }

  last_tick_clock_ = clock_seconds;
  return true;
}

bool VideoPath::finished() const {
  return eof_ && !head_.has_value();
}

}  // namespace avsync
