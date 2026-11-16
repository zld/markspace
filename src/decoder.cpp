#include "avsync/decoder.hpp"

extern "C" {
#include <libavutil/avutil.h>
#include <libavutil/mathematics.h>
}

#include "avsync/limits.hpp"

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
// Drain all frames currently available from the decoder into the bounded
// queue. Returns false on a hard decode error or queue failure.
bool receive_loop(Decoder& decoder, FrameQueue& frames,
                  std::atomic<bool>& stop, bool& decode_failed) {
  AVCodecContext* ctx = decoder.context();
  while (!stop.load()) {
    FramePtr frame = make_frame();
    int rc = avcodec_receive_frame(ctx, frame.get());
    if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) return true;
    if (rc < 0) {
      decode_failed = true;
      return false;
    }

    std::optional<double> pts = decoder.normalize_frame(*frame);
    if (!pts.has_value()) continue;  // Untimestamped frame: skip safely.

    double duration = 0.0;
    if (frame->duration > 0 && frame->duration != AV_NOPTS_VALUE) {
      duration = frame->duration * av_q2d(decoder.time_base());
    } else {
      duration = decoder.frame_duration_seconds();
    }

    const size_t bytes =
        frame_byte_size(decoder.media_type(), *frame);
    DecodedFrame decoded{std::move(frame), *pts, duration};
    if (!frames.push(std::move(decoded), bytes)) return false;
  }
  return true;
}
}  // namespace

bool run_decode(Decoder& decoder, PacketQueue& packets, FrameQueue& frames,
                std::atomic<bool>& stop, std::string& error) {
  AVCodecContext* ctx = decoder.context();
  bool decode_failed = false;

  while (!stop.load()) {
    std::optional<PacketPtr> maybe_packet = packets.pop();
    if (!maybe_packet.has_value()) break;  // Drained + closed, or error.

    int rc = avcodec_send_packet(ctx, maybe_packet->get());
    if (rc < 0 && rc != AVERROR(EAGAIN)) {
      error = "avcodec_send_packet failed on corrupt packet";
      decode_failed = true;
      break;
    }
    if (!receive_loop(decoder, frames, stop, decode_failed)) {
      error = "avcodec_receive_frame failed";
      break;
    }
    if (rc == AVERROR(EAGAIN)) {
      // Decoder input full: retry the same packet after draining output.
      rc = avcodec_send_packet(ctx, maybe_packet->get());
      if (rc < 0) {
        error = "decoder rejected packet after drain";
        decode_failed = true;
        break;
      }
      if (!receive_loop(decoder, frames, stop, decode_failed)) {
        error = "avcodec_receive_frame failed";
        break;
      }
    }
  }

  // End-of-stream flush so buffered frames are emitted before shutdown.
  if (!decode_failed && !stop.load()) {
    avcodec_send_packet(ctx, nullptr);
    receive_loop(decoder, frames, stop, decode_failed);
  }

  if (decode_failed) {
    frames.set_error();
    return false;
  }
  frames.close();
  return !stop.load();
}

}  // namespace avsync
