#include "avsync/audio_path.hpp"

#include <algorithm>
#include <chrono>
#include <vector>

extern "C" {
#include <libavutil/channel_layout.h>
#include <libavutil/opt.h>
}

#include "avsync/limits.hpp"

namespace avsync {
namespace {
// The water-mark queue already caps decoded audio at kAudioLeadSeconds ahead
// of the clock, so the PCM backlog only needs to hold that window plus a small
// margin. Sized generously so the trim below is a defensive no-op in practice.
constexpr double kPcmBufferSeconds = kAudioLeadSeconds + 0.5;
constexpr auto kUnderrunWait = std::chrono::milliseconds(10);
}  // namespace

AudioPath::AudioPath(FrameQueue& frames, MasterClock& clock, Metrics& metrics)
    : frames_(frames), clock_(clock), metrics_(metrics) {
  pcm_capacity_samples_ =
      static_cast<size_t>(kOutSampleRate * kPcmBufferSeconds);
}

AudioPath::~AudioPath() {
  if (swr_) swr_free(&swr_);
}

bool AudioPath::open(std::string* error) {
  if (opened_) return true;

  // Bounded wait for the first frame so startup never reads a transient empty
  // queue as end-of-stream.
  std::optional<DecodedFrame> first =
      frames_.pop_timeout(kUnderrunWait);
  if (!first.has_value()) {
    if (frames_.drained()) {
      eof_ = true;  // Audio stream with zero decodable frames.
      opened_ = true;
    }
    return true;
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
  // Defensive bound: with the water-mark lead and the capacity above this is
  // not expected to trigger; it guarantees the PCM backlog stays bounded even
  // if a malformed stream reports unusual frame sizes.
  const size_t max_floats =
      pcm_capacity_samples_ * static_cast<size_t>(kOutChannels);
  if (pcm_.size() > max_floats) {
    pcm_.erase(pcm_.begin(),
               pcm_.begin() + static_cast<long>(pcm_.size() - max_floats));
  }
  return true;
}

bool AudioPath::pull_frame(bool block, std::string* error) {
  std::optional<DecodedFrame> decoded =
      block ? frames_.pop_timeout(kUnderrunWait) : frames_.try_pop();
  if (!decoded.has_value()) {
    if (frames_.drained()) {
      eof_ = true;
      // First time the decoded stream is exhausted: pull the resampler's
      // delayed tail samples so the final audio is not silently truncated.
      if (!flushed_) {
        flushed_ = true;
        flush_resampler(error);
      }
    }
    return true;
  }
  return resample_into_pcm(*decoded->frame, error);
}

size_t AudioPath::pcm_buffered_per_channel() const {
  std::lock_guard<std::mutex> lock(pcm_mutex_);
  return pcm_.size() / kOutChannels;
}

bool AudioPath::flush_resampler(std::string* error) {
  if (!swr_) return true;
  // NULL input flushes samples delayed inside the resampler.
  const int capacity = 1024;
  std::vector<uint8_t*> out_planes(kOutChannels, nullptr);
  std::vector<float> storage(static_cast<size_t>(capacity) * kOutChannels);
  for (int ch = 0; ch < kOutChannels; ++ch) {
    out_planes[ch] = reinterpret_cast<uint8_t*>(storage.data()) +
                     static_cast<size_t>(ch) * capacity * sizeof(float);
  }
  const int out_samples =
      swr_convert(swr_, out_planes.data(), capacity, nullptr, 0);
  if (out_samples < 0) {
    if (error) *error = "swr_convert flush failed";
    return false;
  }
  std::lock_guard<std::mutex> lock(pcm_mutex_);
  for (int i = 0; i < out_samples; ++i) {
    for (int ch = 0; ch < kOutChannels; ++ch) {
      const float* plane = reinterpret_cast<const float*>(out_planes[ch]);
      pcm_.push_back(plane[i]);
    }
  }
  return true;
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

  // Refill from every frame immediately available, without blocking.
  while (!eof_) {
    if (!pull_frame(/*block=*/false, &error)) {
      fatal_error = true;
      return false;
    }
    if (frames_.size() == 0) break;
  }

  // Cumulative-target consumption: advance the desired media position by the
  // simulated step, then consume exactly the sample delta. Rounding therefore
  // cancels out across ticks instead of truncating a little every tick.
  target_media_seconds_ += simulated_step_seconds;
  const uint64_t target_samples = static_cast<uint64_t>(
      target_media_seconds_ * kOutSampleRate);
  int want = static_cast<int>(
      target_samples > consumed_ ? target_samples - consumed_ : 0);
  if (want == 0) want = 1;

  int take = 0;
  {
    std::lock_guard<std::mutex> lock(pcm_mutex_);
    const size_t available_per_channel = pcm_.size() / kOutChannels;
    take = std::min(want, static_cast<int>(available_per_channel));

    if (take > 0) {
      pcm_.erase(pcm_.begin(),
                 pcm_.begin() +
                     static_cast<long>(take) * kOutChannels);
    }
  }

  if (take > 0) {
    clock_.consume_audio(take);
    metrics_.add_audio_samples(take);
    consumed_ += static_cast<uint64_t>(take);
  } else if (!eof_) {
    // Transient starvation: block briefly for the decoder instead of spinning
    // and letting the wall clock outrun the media timeline.
    if (!pull_frame(/*block=*/true, &error)) {
      fatal_error = true;
      return false;
    }
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
