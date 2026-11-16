#pragma once

#include <atomic>
#include <optional>
#include <string>

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
}

#include "avsync/common.hpp"
#include "avsync/media_queues.hpp"
#include "avsync/queue.hpp"
#include "avsync/sync.hpp"

namespace avsync {

// Owns an AVCodecContext for one stream and converts queued packets into
// timestamp-normalized frames. Flushes the decoder at end-of-stream.
class Decoder {
 public:
  Decoder() = default;
  ~Decoder();
  Decoder(const Decoder&) = delete;
  Decoder& operator=(const Decoder&) = delete;

  bool open(const AVStream* stream, std::string* error);

  AVMediaType media_type() const;
  const AVStream* stream() const { return stream_; }
  AVCodecContext* context() const { return codec_ctx_; }
  AVRational time_base() const;
  double frame_duration_seconds() const;

  // Normalize one received frame onto the zero-based timeline.
  std::optional<double> normalize_frame(const AVFrame& frame);

 private:
  const AVStream* stream_ = nullptr;
  AVCodecContext* codec_ctx_ = nullptr;
  std::optional<TimestampNormalizer> normalizer_;
};

// Decoder thread body. Drains |packets|, flushes at EOS, pushes DecodedFrame
// values into |frames| and propagates decode failures.
bool run_decode(Decoder& decoder, PacketQueue& packets, FrameQueue& frames,
                std::atomic<bool>& stop, std::string& error);

}  // namespace avsync
