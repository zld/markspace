#pragma once

#include <atomic>
#include <memory>
#include <string>
#include <thread>
#include <vector>

#include "avsync/audio_path.hpp"
#include "avsync/clock.hpp"
#include "avsync/decoder.hpp"
#include "avsync/demuxer.hpp"
#include "avsync/limits.hpp"
#include "avsync/metrics.hpp"
#include "avsync/sync.hpp"
#include "avsync/video_path.hpp"

namespace avsync {

// Owns every queue, worker thread and output path, and runs the single
// scheduler loop that drives audio consumption and video presentation off
// the shared master clock.
class Pipeline {
 public:
  explicit Pipeline(const Config& config);
  ~Pipeline();
  Pipeline(const Pipeline&) = delete;
  Pipeline& operator=(const Pipeline&) = delete;

  // Opens the input and starts demux/decode workers. Input problems return
  // false with |error| populated (maps to exit code 2).
  bool setup(std::string* error);
  // Runs until end-of-stream or failure. Decode failures map to exit code 3,
  // thread/queue failures to exit code 4.
  ExitCode run();

  const Metrics& metrics() const { return metrics_; }
  const std::string& input_path() const { return config_.input_path; }

 private:
  bool launch_workers(std::string* error);
  void join_workers();
  void shutdown();

  Config config_;
  std::atomic<bool> stop_{false};

  Demuxer demuxer_;
  std::unique_ptr<Decoder> video_decoder_;
  std::unique_ptr<Decoder> audio_decoder_;
  std::unique_ptr<MasterClock> clock_;
  Synchronizer synchronizer_;
  Metrics metrics_;
  JitterSource jitter_;

  PacketQueue video_packets_{kPacketQueueItems, kPacketQueueBytes};
  PacketQueue audio_packets_{kPacketQueueItems, kPacketQueueBytes};
  FrameQueue video_frames_{kVideoFrameQueueItems, kVideoFrameQueueBytes,
                           kVideoLeadSeconds};
  FrameQueue audio_frames_{kAudioFrameQueueItems, kAudioFrameQueueBytes,
                           kAudioLeadSeconds};

  std::unique_ptr<AudioPath> audio_path_;
  std::unique_ptr<VideoPath> video_path_;

  std::thread demux_thread_;
  std::thread video_decode_thread_;
  std::thread audio_decode_thread_;

  struct WorkerState;
  std::vector<std::shared_ptr<WorkerState>> worker_states_;
  bool workers_started_ = false;
};

}  // namespace avsync
