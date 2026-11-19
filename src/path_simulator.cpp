#include "path_simulator.hpp"

#include <algorithm>
#include <utility>

namespace pwsync {

PathSimulator::PathSimulator(PathConfig config)
    : config_(config),
      clock_(config.clock_mode),
      path_a_("path_a", config.sync_a),
      path_b_("path_b", config.sync_b) {}

void PathSimulator::handleVideo(const NormalizedFrame& frame) {
    // In monotonic mode video drives the master clock; in audio mode the
    // clock only moves with audio, so video never updates it.
    if (config_.clock_mode == ClockMode::Monotonic) {
        clock_.updateReference(frame.pts_seconds, config_.video_nominal_step_seconds,
                               frame.discontinuity);
    }
    path_a_.onVideoFrame(frame.pts_seconds, clock_);
    path_b_.onVideoFrame(frame.pts_seconds, clock_);
}

void PathSimulator::handleAudio(const NormalizedFrame& frame) {
    if (config_.clock_mode == ClockMode::AudioMaster) {
        clock_.updateReference(frame.pts_seconds, config_.audio_nominal_step_seconds,
                               frame.discontinuity);
    }
    path_a_.onAudioFrame(frame.pts_seconds, clock_);
    path_b_.onAudioFrame(frame.pts_seconds, clock_);
}

void PathSimulator::pump() {
    // Merge the two per-stream queues in PTS order so every synchronizer
    // decision observes a chronologically consistent master clock.
    while (video_cursor_ < pending_video_.size() ||
           audio_cursor_ < pending_audio_.size()) {
        const bool have_video = video_cursor_ < pending_video_.size();
        const bool have_audio = audio_cursor_ < pending_audio_.size();
        if (have_video && (!have_audio ||
                           pending_video_[video_cursor_].pts_seconds <=
                               pending_audio_[audio_cursor_].pts_seconds)) {
            handleVideo(pending_video_[video_cursor_++]);
        } else {
            handleAudio(pending_audio_[audio_cursor_++]);
        }
    }
    // Reclaim consumed storage while keeping the queues usable.
    if (video_cursor_ > 0) {
        pending_video_.erase(pending_video_.begin(),
                             pending_video_.begin() +
                                 static_cast<std::ptrdiff_t>(video_cursor_));
        video_cursor_ = 0;
    }
    if (audio_cursor_ > 0) {
        pending_audio_.erase(pending_audio_.begin(),
                             pending_audio_.begin() +
                                 static_cast<std::ptrdiff_t>(audio_cursor_));
        audio_cursor_ = 0;
    }
}

void PathSimulator::ingest(const NormalizedFrame& frame) {
    if (frame.type == MediaType::Video) {
        pending_video_.push_back(frame);
        ++ingested_video_;
    } else if (frame.type == MediaType::Audio) {
        pending_audio_.push_back(frame);
        ++ingested_audio_;
    } else {
        return;
    }
    pump();
}

void PathSimulator::finalize(SyncReport& report) {
    path_a_.finalize();
    path_b_.finalize();
    report.clock = clock_.stats();
    report.path_a = path_a_.stats();
    report.path_b = path_b_.stats();
}

}  // namespace pwsync
