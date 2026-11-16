#pragma once

#include <deque>
#include <memory>
#include <mutex>
#include <optional>

extern "C" {
#include <libswresample/swresample.h>
}

#include "avsync/clock.hpp"
#include "avsync/common.hpp"
#include "avsync/media_queues.hpp"
#include "avsync/metrics.hpp"

namespace avsync {

// Consumes decoded audio frames, resamples them to a fixed stereo/float PCM
// format and drains a bounded internal PCM queue. Consumed samples advance the
// master clock; queue starvation is reported back as an underrun so the clock
// can hand over to the monotonic follower.
class AudioPath {
 public:
  static constexpr int kOutSampleRate = 48000;
  static constexpr int kOutChannels = 2;

  AudioPath(FrameQueue& frames, MasterClock& clock, Metrics& metrics);
  ~AudioPath();
  AudioPath(const AudioPath&) = delete;
  AudioPath& operator=(const AudioPath&) = delete;

  // Lazily create the resampler from the first decoded frame.
  bool open(std::string* error);

  // One scheduler tick: make queued PCM available, then consume samples up to
  // the simulated time budget. Returns false on a resampling failure.
  bool update(double simulated_step_seconds, bool& fatal_error);

  bool finished() const;
  uint64_t total_consumed_samples() const;

 private:
  bool pull_frame(bool block, std::string* error);
  bool resample_into_pcm(AVFrame& frame, std::string* error);
  // Drain samples buffered inside the resampler at end-of-stream.
  bool flush_resampler(std::string* error);
  size_t pcm_buffered_per_channel() const;

  FrameQueue& frames_;
  MasterClock& clock_;
  Metrics& metrics_;

  SwrContext* swr_ = nullptr;
  bool opened_ = false;
  bool eof_ = false;

  // Bounded PCM backlog: planar float samples, one float per channel slot.
  std::deque<float> pcm_;
  mutable std::mutex pcm_mutex_;
  size_t pcm_capacity_samples_ = 0;  // Per channel.

  uint64_t consumed_ = 0;
  uint64_t underruns_ = 0;
  bool flushed_ = false;
  double target_media_seconds_ = 0.0;  // Cumulative time the clock should reach.
};

}  // namespace avsync
