#include "avsync/audio_path.hpp"

#include <algorithm>
#include <vector>

extern "C" {
#include <libavutil/channel_layout.h>
#include <libavutil/opt.h>
}

#include "avsync/limits.hpp"

namespace avsync {
namespace {
constexpr size_t kMaxPcmBufferedMs = 500;  // Per channel, bounded backlog.
}

AudioPath::AudioPath(FrameQueue& frames, MasterClock& clock, Metrics& metrics,
                     JitterSource& jitter)
    : frames_(frames), clock_(clock), metrics_(metrics), jitter_(jitter) {
  pcm_capacity_samples_ =
      static_cast<size_t>(kOutSampleRate) * kMaxPcmBufferedMs / 1000;
}

AudioPath::~AudioPath() {
  if (swr_) swr_free(&swr_);
}

bool AudioPath::open(std::string* error) {
  if (opened_) return true;

  std::optional<DecodedFrame> first = frames_.try_pop();
  if (!first.has_value()) {
    if (frames_.drained()) {
      eof_ = true;  // Audio stream with zero decodable frames.
      opened_ = true;
      return true;
    }
    return true;  // Nothing ready yet; retry on the next tick.
  }

  AVChannelLayout out_layout;
  av_channel_layout_default(&out_layout, kOutChannels);
  int rc = swr_alloc_set_opts2(
      &swr_, &out_layout, AV_SAMPLE_FMT_FLTP, kOutSampleRate,
      &first->frame->ch_layout,
      static_cast<AVSampleFormat>(first->frame->format),
      first->frame->sample_rate, 0, nullptr);
  av_channel_layout_uninit(&out_layout);
  if (rc < 0 || !swr_) {
    if (error) *error = "swr_alloc_set_opts2 failed for audio resampling";
    return false;
  }
  rc = swr_init(swr_);
  if (rc < 0) {
    if (error) *error = "swr_init failed";
    return false;
  }
  opened_ = true;
  if (!resample_into_pcm(*first->frame, error)) return false;
  return true;
}

bool AudioPath::resample_into_pcm(AVFrame& frame, std::string* error) {
  const int capacity = frame.nb_samples + 256;
  std::vector<uint8_t*> out_planes(kOutChannels, nullptr);
  std::vector<float> storage(static_cast<size_t>(capacity) * kOutChannels);
  for (int ch = 0; ch < kOutChannels; ++ch) {
    out_planes[ch] = reinterpret_cast<uint8_t*>(storage.data()) +
                     static_cast<size_t>(ch) * capacity * sizeof(float);
  }

  const int out_samples = swr_convert(
      swr_, out_planes.data(), capacity,
      const_cast<const uint8_t**>(frame.extended_data), frame.nb_samples);
  if (out_samples < 0) {
    if (error) *error = "swr_convert failed on audio frame";
    return false;
  }

  std::lock_guard<std::mutex> lock(pcm_mutex_);
  for (int i = 0; i < out_samples; ++i) {
    for (int ch = 0; ch < kOutChannels; ++ch) {
      const float* plane = reinterpret_cast<const float*>(out_planes[ch]);
      pcm_.push_back(plane[i]);  // Interleaved storage: L,R,L,R,...
    }
  }
  // Backpressure: never let the PCM backlog exceed the bounded capacity.
  const size_t max_floats =
      pcm_capacity_samples_ * static_cast<size_t>(kOutChannels);
  if (pcm_.size() > max_floats) {
    pcm_.erase(pcm_.begin(),
               pcm_.begin() + static_cast<long>(pcm_.size() - max_floats));
  }
  return true;
}

bool AudioPath::pull_frame(std::string* error) {
  std::optional<DecodedFrame> decoded = frames_.try_pop();
  if (!decoded.has_value()) {
    if (frames_.drained()) eof_ = true;
    return true;
  }
  return resample_into_pcm(*decoded->frame, error);
}

bool AudioPath::update(double simulated_step_seconds, bool& fatal_error) {
  fatal_error = false;
  std::string error;
  if (!opened_) {
    if (!open(&error)) {
      fatal_error = true;
      return false;
    }
    if (!opened_) return true;  // Still waiting for the first frame.
  }

  // Keep the PCM backlog fed while decoded frames are immediately available.
  for (int probes = 0; probes < 4 && !eof_; ++probes) {
    if (!pull_frame(&error)) {
      fatal_error = true;
      return false;
    }
  }

  const int64_t jitter_us = jitter_.next_delay_us();
  if (jitter_us > 0) sleep_ns(jitter_us * 1000);

  const int wanted = std::max(
      0, static_cast<int>(simulated_step_seconds * kOutSampleRate));
  int want = wanted > 0 ? wanted : 1;

  std::lock_guard<std::mutex> lock(pcm_mutex_);
  const size_t available_per_channel = pcm_.size() / kOutChannels;
  const int take = std::min(want, static_cast<int>(available_per_channel));

  if (take > 0) {
    pcm_.erase(pcm_.begin(),
               pcm_.begin() +
                   static_cast<long>(take) * kOutChannels);
    clock_.consume_audio(take);
    metrics_.add_audio_samples(take);
    consumed_ += static_cast<uint64_t>(take);
  } else if (!eof_) {
    // Decoder starvation: the monotonic follower keeps the timeline moving.
    clock_.notify_audio_underrun();
    metrics_.add_audio_underrun();
    ++underruns_;
  }
  return true;
}

bool AudioPath::finished() const {
  std::lock_guard<std::mutex> lock(pcm_mutex_);
  return eof_ && pcm_.empty();
}

uint64_t AudioPath::total_consumed_samples() const { return consumed_; }

}  // namespace avsync
