#pragma once

#include <optional>

#include "avsync/common.hpp"
#include "avsync/media_queues.hpp"
#include "avsync/metrics.hpp"
#include "avsync/sync.hpp"

namespace avsync {

// Schedules decoded video frames against the shared master clock: waits while
// early, drops frames past the timeout, and repeats the previous frame to fill
// empty slots. No rendering happens — "presenting" only updates bookkeeping.
class VideoPath {
 public:
  VideoPath(FrameQueue& frames, const Synchronizer& synchronizer,
            Metrics& metrics);

  // One scheduler tick driven by the master clock. Returns false on queue
  // error; sets |fatal_error| when the frame queue signalled a decode failure.
  bool update(double clock_seconds, bool& fatal_error);

  bool finished() const;
  uint64_t shown() const { return shown_; }
  uint64_t dropped() const { return dropped_; }
  uint64_t repeated() const { return repeated_; }

 private:
  void record_delta(double frame_pts, double clock_seconds);

  FrameQueue& frames_;
  const Synchronizer& sync_;
  Metrics& metrics_;

  std::optional<DecodedFrame> head_;       // Poped-but-not-yet-shown frame.
  std::optional<double> last_shown_pts_;
  double last_tick_clock_ = -1.0;

  bool eof_ = false;
  uint64_t shown_ = 0;
  uint64_t dropped_ = 0;
  uint64_t repeated_ = 0;
};

}  // namespace avsync
