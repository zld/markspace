#include "avsync/demuxer.hpp"

#include <chrono>
#include <cstring>
#include <thread>

extern "C" {
#include <libavutil/avutil.h>
}

namespace avsync {
namespace {
// Trampoline so a blocked network-less file read still observes stop requests.
int interrupt_cb(void* opaque) {
  auto* stop = static_cast<std::atomic<bool>*>(opaque);
  return stop && stop->load() ? 1 : 0;
}
}  // namespace

Demuxer::~Demuxer() {
  if (fmt_) avformat_close_input(&fmt_);
}

bool Demuxer::open(const std::string& path, std::atomic<bool>& stop,
                   std::string* error) {
  fmt_ = avformat_alloc_context();
  if (!fmt_) {
    if (error) *error = "failed to allocate format context";
    return false;
  }
  fmt_->interrupt_callback.opaque = &stop;
  fmt_->interrupt_callback.callback = interrupt_cb;

  int rc = avformat_open_input(&fmt_, path.c_str(), nullptr, nullptr);
  if (rc < 0) {
    if (error) *error = "cannot open input '" + path + "': not a media file";
    return false;
  }
  rc = avformat_find_stream_info(fmt_, nullptr);
  if (rc < 0) {
    if (error) *error = "input contains no decodable streams";
    return false;
  }

  for (unsigned i = 0; i < fmt_->nb_streams; ++i) {
    AVCodecParameters* par = fmt_->streams[i]->codecpar;
    if (par->codec_type == AVMEDIA_TYPE_VIDEO && video_index_ < 0) {
      video_index_ = static_cast<int>(i);
    } else if (par->codec_type == AVMEDIA_TYPE_AUDIO && audio_index_ < 0) {
      audio_index_ = static_cast<int>(i);
    }
  }
  if (video_index_ < 0 && audio_index_ < 0) {
    if (error) *error = "input has neither video nor audio stream";
    return false;
  }
  return true;
}

double Demuxer::duration_seconds() const {
  if (!fmt_ || fmt_->duration <= 0) return 0.0;
  return static_cast<double>(fmt_->duration) / AV_TIME_BASE;
}

bool run_demux(Demuxer& demuxer, const DemuxSinks& sinks,
               std::atomic<bool>& stop, std::string& error) {
  AVFormatContext* fmt = demuxer.format_context();

  // The water-marked frame queues throttle the decoders to a bounded lead over
  // the playback clock, so the bounded packet queues below only ever buffer a
  // short window: blocking here is bounded and cannot run to end-of-stream
  // ahead of playback. A 1 ms yield keeps the stop flag responsive.
  while (!stop.load()) {
    PacketPtr packet = make_packet();
    int rc = av_read_frame(fmt, packet.get());
    if (rc == AVERROR_EOF) {
      break;
    }
    if (rc < 0) {
      error = "demux read failed (corrupt input?)";
      sinks.video->set_error();
      sinks.audio->set_error();
      return false;
    }

    PacketQueue* target = nullptr;
    if (packet->stream_index == demuxer.video_index()) {
      target = sinks.video;
    } else if (packet->stream_index == demuxer.audio_index()) {
      target = sinks.audio;
    }
    if (!target) {
      continue;  // Subtitle/data packets are released by the RAII wrapper.
    }

    const size_t bytes = packet_byte_size(*packet);
    while (!target->try_push(packet, bytes)) {
      if (stop.load() || target->closed() || target->error()) {
        sinks.video->close();
        sinks.audio->close();
        return !stop.load();
      }
      std::this_thread::sleep_for(std::chrono::milliseconds(1));
    }
  }

  sinks.video->close();
  sinks.audio->close();
  return !stop.load();
}

}  // namespace avsync
