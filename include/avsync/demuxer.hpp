#pragma once

#include <atomic>
#include <string>

extern "C" {
#include <libavformat/avformat.h>
}

#include "avsync/common.hpp"
#include "avsync/media_queues.hpp"
#include "avsync/queue.hpp"

namespace avsync {

// Thin RAII wrapper around AVFormatContext: opens a local file, locates the
// best video/audio streams and exposes interruptible packet reads.
class Demuxer {
 public:
  Demuxer() = default;
  ~Demuxer();
  Demuxer(const Demuxer&) = delete;
  Demuxer& operator=(const Demuxer&) = delete;

  // Returns false with a human-readable reason on missing/invalid input.
  bool open(const std::string& path, std::atomic<bool>& stop,
            std::string* error);

  int video_index() const { return video_index_; }
  int audio_index() const { return audio_index_; }
  bool has_video() const { return video_index_ >= 0; }
  bool has_audio() const { return audio_index_ >= 0; }

  AVStream* stream(int index) const { return fmt_->streams[index]; }
  AVFormatContext* format_context() const { return fmt_; }
  double duration_seconds() const;

 private:
  AVFormatContext* fmt_ = nullptr;
  int video_index_ = -1;
  int audio_index_ = -1;
};

struct DemuxSinks {
  PacketQueue* video = nullptr;
  PacketQueue* audio = nullptr;
};

// Demux thread body. Routes packets into per-stream bounded queues, closes
// them at end-of-stream and propagates I/O failures. Returns false only on
// read error or external stop.
bool run_demux(Demuxer& demuxer, const DemuxSinks& sinks,
               std::atomic<bool>& stop, std::string& error);

}  // namespace avsync
