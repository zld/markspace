#include "avsync/common.hpp"

#include <algorithm>
#include <chrono>
#include <thread>

extern "C" {
#include <libavutil/imgutils.h>
#include <libavutil/samplefmt.h>
}

namespace avsync {
namespace {
uint64_t splitmix64(uint64_t& x) {
  uint64_t z = (x += 0x9e3779b97f4a7c15ULL);
  z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9ULL;
  z = (z ^ (z >> 27)) * 0x94d049bb133111ebULL;
  return z ^ (z >> 31);
}
}  // namespace

JitterSource::JitterSource(int max_jitter_ms, uint64_t seed)
    : max_jitter_ms_(std::max(0, max_jitter_ms)),
      state_(seed == 0 ? 0x123456789abcdef0ULL : seed) {}

int64_t JitterSource::next_delay_us() {
  if (max_jitter_ms_ == 0) return 0;
  const uint64_t r = splitmix64(state_);
  return static_cast<int64_t>(r % static_cast<uint64_t>(max_jitter_ms_ * 1000));
}

PacketPtr make_packet() {
  return PacketPtr(av_packet_alloc(),
                   [](AVPacket* p) { av_packet_free(&p); });
}

FramePtr make_frame() {
  return FramePtr(av_frame_alloc(), [](AVFrame* f) { av_frame_free(&f); });
}

size_t packet_byte_size(const AVPacket& packet) {
  return static_cast<size_t>(std::max(0, packet.size) +
                             packet.side_data_elems * 64);
}

size_t frame_byte_size(AVMediaType type, const AVFrame& frame) {
  if (type == AVMEDIA_TYPE_VIDEO) {
    int size = av_image_get_buffer_size(static_cast<AVPixelFormat>(frame.format),
                                        frame.width, frame.height, 1);
    if (size > 0) return static_cast<size_t>(size);
  }
  if (type == AVMEDIA_TYPE_AUDIO) {
    const int linesize = frame.linesize[0] > 0 ? frame.linesize[0] : 0;
    return static_cast<size_t>(linesize > 0
                                   ? linesize * frame.ch_layout.nb_channels
                                   : frame.nb_samples * frame.ch_layout.nb_channels);
  }
  return 0;
}

int64_t now_ns() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
             std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

void sleep_ns(int64_t ns) {
  if (ns <= 0) return;
  std::this_thread::sleep_for(std::chrono::nanoseconds(ns));
}

int64_t seconds_to_ns(double seconds) {
  return static_cast<int64_t>(seconds * 1'000'000'000.0);
}

}  // namespace avsync
