#pragma once

#include <cstdint>
#include <vector>

#include "media_types.hpp"

namespace pwsync {

enum class LatePolicy : std::uint8_t {
    Drop = 0,    // discard a video frame that is already too late
    Repeat = 1,  // keep showing the previous video frame instead
};

struct SynchronizerConfig {
    LatePolicy policy = LatePolicy::Drop;
    double late_threshold_seconds = 0.035;
    double path_delay_seconds = 0.0;  // processing latency added by this path
};

enum class SyncAction : std::uint8_t {
    Present = 0,  // show the frame now
    Wait = 1,     // frame is early: hold until its presentation time
    Drop = 2,     // frame is too late: discard it
    Repeat = 3,   // frame is too late: repeat the previous frame instead
};

const char* toString(SyncAction action);

// One synchronizer instance per simulated output path. Both paths share the
// same MasterClock reference; only configuration and statistics differ.
class Synchronizer {
public:
    Synchronizer(std::string path_name, SynchronizerConfig config);

    SyncAction onVideoFrame(double frame_pts_seconds, const class MasterClock& clock);
    void onAudioFrame(double frame_pts_seconds, const class MasterClock& clock);

    const PathStats& stats() const { return stats_; }
    const SynchronizerConfig& config() const { return config_; }

    // Finish percentile/mean/max aggregation from collected offset samples.
    void finalize();

private:
    void recordOffset(double offset_seconds);

    SynchronizerConfig config_;
    PathStats stats_;
    bool has_previous_video_ = false;
    std::vector<double> offset_samples_ms_;
};

}  // namespace pwsync
