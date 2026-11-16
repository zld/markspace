#include "avsync/pipeline.hpp"

#include <algorithm>
#include <exception>

#include "avsync/limits.hpp"

namespace avsync {
namespace {
constexpr double kTickRealSeconds = 0.002;
constexpr double kShutdownGraceSeconds = 8.0;
}  // namespace

struct Pipeline::WorkerState {
  bool ok = true;                  // Thread body returned successfully.
  bool decode_failure = false;     // Demux/decode reported a media error.
  bool thread_exception = false;   // An exception escaped the thread body.
  std::string error;
};

Pipeline::Pipeline(const Config& config)
    : config_(config), jitter_(config.jitter_ms, config.seed) {}

Pipeline::~Pipeline() {
  shutdown();
  join_workers();
}

bool Pipeline::setup(std::string* error) {
  if (config_.input_path.empty()) {
    if (error) *error = "missing --input <file>";
    return false;
  }
  if (config_.realtime_factor <= 0.0) {
    if (error) *error = "--realtime-factor must be positive";
    return false;
  }
  if (!demuxer_.open(config_.input_path, stop_, error)) return false;

  SyncPolicy policy;
  if (demuxer_.has_video()) {
    video_decoder_ = std::make_unique<Decoder>();
    if (!video_decoder_->open(demuxer_.stream(demuxer_.video_index()),
                              error)) {
      return false;
    }
    policy.frame_duration_seconds = video_decoder_->frame_duration_seconds();
  }
  int audio_rate = 0;
  if (demuxer_.has_audio()) {
    audio_decoder_ = std::make_unique<Decoder>();
    if (!audio_decoder_->open(demuxer_.stream(demuxer_.audio_index()),
                              error)) {
      return false;
    }
    audio_rate = audio_decoder_->context()->sample_rate;
  }

  clock_ = std::make_unique<MasterClock>(audio_rate);
  synchronizer_ = Synchronizer(policy);

  if (audio_decoder_) {
    audio_path_ =
        std::make_unique<AudioPath>(audio_frames_, *clock_, metrics_);
  }
  if (video_decoder_) {
    video_path_ = std::make_unique<VideoPath>(video_frames_, synchronizer_,
                                              metrics_);
  }
  if (!audio_path_ && !video_path_) {
    if (error) *error = "input has no playable audio or video";
    return false;
  }
  return true;
}

bool Pipeline::launch_workers(std::string* error) {
  auto demux_state = std::make_shared<WorkerState>();
  auto video_state = std::make_shared<WorkerState>();
  auto audio_state = std::make_shared<WorkerState>();
  worker_states_ = {demux_state, video_state, audio_state};

  try {
    demux_thread_ = std::thread([this, demux_state] {
      try {
        DemuxSinks sinks{&video_packets_, &audio_packets_};
        demux_state->ok =
            run_demux(demuxer_, sinks, stop_, demux_state->error);
        demux_state->decode_failure = !demux_state->ok;
      } catch (const std::exception& ex) {
        demux_state->thread_exception = true;
        demux_state->error = ex.what();
        stop_.store(true);
      }
    });

    if (video_decoder_) {
      video_decode_thread_ = std::thread([this, video_state] {
        try {
          video_state->ok = run_decode(*video_decoder_, video_packets_,
                                       video_frames_, stop_,
                                       video_state->error);
          video_state->decode_failure = video_frames_.error();
        } catch (const std::exception& ex) {
          video_state->thread_exception = true;
          video_state->error = ex.what();
          stop_.store(true);
        }
      });
    }
    if (audio_decoder_) {
      audio_decode_thread_ = std::thread([this, audio_state] {
        try {
          audio_state->ok = run_decode(*audio_decoder_, audio_packets_,
                                       audio_frames_, stop_,
                                       audio_state->error);
          audio_state->decode_failure = audio_frames_.error();
        } catch (const std::exception& ex) {
          audio_state->thread_exception = true;
          audio_state->error = ex.what();
          stop_.store(true);
        }
      });
    }
  } catch (const std::exception& ex) {
    if (error) {
      *error = std::string("failed to start worker thread: ") + ex.what();
    }
    shutdown();
    return false;
  }

  workers_started_ = true;
  return true;
}

void Pipeline::shutdown() {
  stop_.store(true);
  video_packets_.set_error();
  audio_packets_.set_error();
  video_frames_.set_error();
  audio_frames_.set_error();
}

void Pipeline::join_workers() {
  if (demux_thread_.joinable()) demux_thread_.join();
  if (video_decode_thread_.joinable()) video_decode_thread_.join();
  if (audio_decode_thread_.joinable()) audio_decode_thread_.join();
  workers_started_ = false;
}

ExitCode Pipeline::run() {
  std::string launch_error;
  if (!workers_started_ && !launch_workers(&launch_error)) {
    return ExitCode::ThreadFailure;
  }

  clock_->start(config_.realtime_factor);
  const double factor = config_.realtime_factor;

  // No blocking pre-roll: the scheduler starts immediately so both output
  // paths drain their queues from the first tick. Transient audio starvation
  // is absorbed inside AudioPath by a bounded wait, which cannot stall the
  // demuxer the way a pre-roll fill could.

  // Wall-time allowance is the media duration divided by the speed factor,
  // plus grace for decoding and bounded scheduler sleeps.
  const double media_duration =
      demuxer_.duration_seconds() > 0 ? demuxer_.duration_seconds() : 30.0;
  const double wall_deadline =
      clock_->wall_seconds() + media_duration / factor +
          kShutdownGraceSeconds;

  bool fatal = false;
  int64_t prev_tick_ns = now_ns();
  while (!stop_.load()) {
    const int64_t tick_start = now_ns();
    // Publish the playback position so water-marked frame queues throttle
    // their decoders to a bounded lead over real time.
    const double media_now = clock_->now_seconds();
    video_frames_.set_clock_seconds(media_now);
    audio_frames_.set_clock_seconds(media_now);

    // Audio consumption follows the real (jitter-perturbed) tick duration, so
    // the sample-driven clock tracks wall time * factor instead of assuming a
    // perfectly periodic scheduler.
    const double real_step =
        static_cast<double>(tick_start - prev_tick_ns) / 1.0e9;
    prev_tick_ns = tick_start;

    if (audio_path_) {
      bool audio_fatal = false;
      if (!audio_path_->update(real_step * factor, audio_fatal) &&
          audio_fatal) {
        fatal = true;
        shutdown();
        break;
      }
    }
    if (video_path_) {
      bool video_fatal = false;
      if (!video_path_->update(clock_->now_seconds(), video_fatal) &&
          video_fatal) {
        fatal = true;
        shutdown();
        break;
      }
    }
    metrics_.record_drift(clock_->drift_ms());

    const bool video_done = !video_path_ || video_path_->finished();
    const bool audio_done = !audio_path_ || audio_path_->finished();
    if (video_done && audio_done) break;
    if (clock_->wall_seconds() > wall_deadline) break;

    // Non-blocking tick throttle. Jitter perturbs the scheduling cadence so
    // races are reproducible with a fixed seed; it is added once per tick
    // rather than inside a path, keeping both outputs driven from this loop.
    const int64_t elapsed = now_ns() - tick_start;
    const int64_t jitter_ns = jitter_.next_delay_us() * 1000;
    sleep_ns(std::max<int64_t>(
        0, seconds_to_ns(kTickRealSeconds) + jitter_ns - elapsed));
  }

  stop_.store(true);
  video_packets_.close();
  audio_packets_.close();
  video_frames_.close();
  audio_frames_.close();
  join_workers();

  metrics_.set_duration_seconds(clock_->now_seconds());

  for (const auto& state : worker_states_) {
    if (state && state->thread_exception) return ExitCode::ThreadFailure;
  }
  for (const auto& state : worker_states_) {
    if (state && state->decode_failure) return ExitCode::DecodeFailure;
  }
  if (fatal) return ExitCode::ThreadFailure;
  return ExitCode::Ok;
}

}  // namespace avsync
