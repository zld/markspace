#pragma once

#include <cstdint>
#include <memory>
#include <optional>
#include <string>

extern "C" {
#include <libavutil/avutil.h>
#include <libavutil/frame.h>
#include <libavutil/rational.h>
#include <libavcodec/packet.h>
}

namespace avsync {

// Process exit codes contracted by the CLI.
enum class ExitCode : int {
  Ok = 0,
  UsageOrInput = 2,
  DecodeFailure = 3,
  ThreadFailure = 4,
};

// Everything the pipeline needs, parsed and validated from the command line.
struct Config {
  std::string input_path;
  std::optional<std::string> json_path;
  int jitter_ms = 0;       // Upper bound of injected scheduling jitter.
  uint64_t seed = 0;       // Fixed seed => reproducible jitter.
  double realtime_factor = 1.0;  // 2.0 means consume the timeline twice as fast.
};

// Deterministic jitter source so scheduling races can be replayed with --seed.
class JitterSource {
 public:
  JitterSource(int max_jitter_ms, uint64_t seed);

  // Returns a jitter duration in microseconds, uniformly drawn from [0, max].
  int64_t next_delay_us();

 private:
  int max_jitter_ms_;
  uint64_t state_;
};

// RAII ownership wrappers keep AVPacket/AVFrame lifecycle management explicit.
using PacketPtr = std::unique_ptr<AVPacket, void (*)(AVPacket*)>;
using FramePtr = std::unique_ptr<AVFrame, void (*)(AVFrame*)>;

PacketPtr make_packet();
FramePtr make_frame();

// Best-effort decoded-frame footprint, used to bound queue memory.
size_t frame_byte_size(AVMediaType type, const AVFrame& frame);
// Wire-size approximation for a queued compressed packet.
size_t packet_byte_size(const AVPacket& packet);

// Steady-clock helpers; all durations are in nanoseconds.
int64_t now_ns();
void sleep_ns(int64_t ns);
int64_t seconds_to_ns(double seconds);

}  // namespace avsync
