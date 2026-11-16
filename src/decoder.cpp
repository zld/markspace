#include "avsync/decoder.hpp"

#include <chrono>
#include <thread>

extern "C" {
#include <libavutil/avutil.h>
#include <libavutil/mathematics.h>
}

#include "avsync/limits.hpp"

#include <chrono>
#include <thread>

namespace avsync {

Decoder::~Decoder() {
  if (codec_ctx_) avcodec_free_context(&codec_ctx_);
}

bool Decoder::open(const AVStream* stream, std::string* error) {
  stream_ = stream;
  const AVCodec* codec = avcodec_find_decoder(stream->codecpar->codec_id);
  if (!codec) {
    if (error) *error = "no decoder for stream codec";
    return false;
  }
  codec_ctx_ = avcodec_alloc_context3(codec);
  if (!codec_ctx_) {
    if (error) *error = "failed to allocate decoder context";
    return false;
  }
  int rc = avcodec_parameters_to_context(codec_ctx_, stream->codecpar);
  if (rc < 0) {
    if (error) *error = "failed to copy codec parameters";
    return false;
  }
  rc = avcodec_open2(codec_ctx_, codec, nullptr);
  if (rc < 0) {
    if (error) *error = "decoder failed to open";
    return false;
  }
  normalizer_.emplace(stream->time_base);
  return true;
}

AVMediaType Decoder::media_type() const {
  return codec_ctx_ ? codec_ctx_->codec_type : AVMEDIA_TYPE_UNKNOWN;
}

AVRational Decoder::time_base() const {
  return codec_ctx_ ? codec_ctx_->pkt_timebase : stream_->time_base;
}

double Decoder::frame_duration_seconds() const {
  AVRational frame_rate = stream_->avg_frame_rate;
  if (frame_rate.num > 0 && frame_rate.den > 0) {
    return av_q2d(av_inv_q(frame_rate));
  }
  if (stream_->time_base.num > 0) {
    return av_q2d(stream_->time_base);
  }
  return media_type() == AVMEDIA_TYPE_VIDEO ? 1.0 / 30.0 : 0.0;
}

std::optional<double> Decoder::normalize_frame(const AVFrame& frame) {
  return normalizer_->normalize(frame.pts, frame.pkt_dts,
                                frame_duration_seconds());
}

namespace {
enum class ReceiveResult { Done, Backpressure, Error };

// Drain all frames currently available from the decoder into the bounded
// queue. Backpressure (queue full but healthy) is distinct from a hard error
// so the caller can wait and retry rather than flushing the decoder early.
ReceiveResult receive_loop(Decoder& decoder, FrameQueue& frames,
                           std::atomic<bool>& stop) {
  AVCodecContext* ctx = decoder.context();
  while (!stop.load()) {
    FramePtr frame = make_frame();
    int rc = avcodec_receive_frame(ctx, frame.get());
    if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) {
      return ReceiveResult::Done;
    }
    if (rc < 0) {
      return ReceiveResult::Error;
    }

    std::optional<double> pts = decoder.normalize_frame(*frame);
    if (!pts.has_value()) continue;  // Untimestamped frame: skip safely.

    double duration = 0.0;
    if (frame->duration > 0 && frame->duration != AV_NOPTS_VALUE) {
      duration = frame->duration * av_q2d(decoder.time_base());
    } else {
      duration = decoder.frame_duration_seconds();
    }

    const size_t bytes = frame_byte_size(decoder.media_type(), *frame);
    DecodedFrame decoded{std::move(frame), *pts, duration};
    // The water-mark queue blocks while the decoder is too far ahead of the
    // playback clock, throttling the whole producer chain to real time. It
    // only returns false on close/error/stop.
    if (!frames.push(std::move(decoded), bytes, stop)) {
      return frames.error() ? ReceiveResult::Error
                            : ReceiveResult::Backpressure;
    }
  }
  return ReceiveResult::Done;
}
}  // namespace

bool run_decode(Decoder& decoder, PacketQueue& packets, FrameQueue& frames,
                std::atomic<bool>& stop, std::string& error) {
  AVCodecContext* ctx = decoder.context();

  while (!stop.load()) {
    std::optional<PacketPtr> maybe_packet = packets.pop();
    if (!maybe_packet.has_value()) {
      break;  // Drained + closed, or error.
    }

    int rc = avcodec_send_packet(ctx, maybe_packet->get());
    if (rc < 0 && rc != AVERROR(EAGAIN)) {
      error = "avcodec_send_packet failed on corrupt packet";
      frames.set_error();
      return false;
    }

    ReceiveResult result = receive_loop(decoder, frames, stop);
    if (result == ReceiveResult::Error) {
      error = "avcodec_receive_frame failed";
      frames.set_error();
      return false;
    }

    if (rc == AVERROR(EAGAIN)) {
      // Decoder input was full. After draining its output above, resend the
      // same packet; it must not be lost.
      rc = avcodec_send_packet(ctx, maybe_packet->get());
      if (rc < 0 && rc != AVERROR(EAGAIN)) {
        error = "decoder rejected packet after drain";
        frames.set_error();
        return false;
      }
      result = receive_loop(decoder, frames, stop);
      if (result == ReceiveResult::Error) {
        error = "avcodec_receive_frame failed";
        frames.set_error();
        return false;
      }
    }
  }

  // End-of-stream flush so buffered frames are emitted before shutdown.
  if (!stop.load() && !frames.error()) {
    avcodec_send_packet(ctx, nullptr);
    receive_loop(decoder, frames, stop);
  }

  frames.close();
  return !stop.load();
}

}  // namespace avsync
