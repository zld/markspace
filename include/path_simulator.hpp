#pragma once

#include <vector>

#include "clock.hpp"
#include "media_types.hpp"
#include "synchronizer.hpp"

namespace pwsync {

struct PathConfig {
    SynchronizerConfig sync_a;
    SynchronizerConfig sync_b;
    ClockMode clock_mode = ClockMode::AudioMaster;
    double discontinuity_tolerance_seconds = 0.25;
    double video_nominal_step_seconds = 1.0 / 30.0;
    double audio_nominal_step_seconds = 1024.0 / 48000.0;
};

// Drives two simulated output paths over one decoded frame stream. Both paths
// share a single MasterClock; only per-path processing delay and late policy
// differ. Fully deterministic: no wall clock, no sleeping.
class PathSimulator {
public:
    explicit PathSimulator(PathConfig config);

    // Feed every decoded frame in decode order. Ordering between the two
    // streams is resolved internally against the shared clock.
    void ingest(const NormalizedFrame& frame);

    // Finalize percentile aggregation and return the two path reports.
    void finalize(SyncReport& report);

    const MasterClock& clock() const { return clock_; }
    const Synchronizer& pathA() const { return path_a_; }
    const Synchronizer& pathB() const { return path_b_; }
    std::uint64_t ingestedVideo() const { return ingested_video_; }
    std::uint64_t ingestedAudio() const { return ingested_audio_; }

private:
    PathConfig config_;
    MasterClock clock_;
    Synchronizer path_a_;
    Synchronizer path_b_;
    std::vector<NormalizedFrame> pending_video_;
    std::vector<NormalizedFrame> pending_audio_;
    std::size_t video_cursor_ = 0;
    std::size_t audio_cursor_ = 0;
    std::uint64_t ingested_video_ = 0;
    std::uint64_t ingested_audio_ = 0;

    void pump();
    void handleVideo(const NormalizedFrame& frame);
    void handleAudio(const NormalizedFrame& frame);
};

}  // namespace pwsync
