#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace pwsync {

struct FixtureHazard {
    // PTS (seconds, on the video timeline) where a timestamp jump is inserted.
    double at_seconds = 0.0;
    // Forward shift applied to all subsequent video PTS, in seconds. MP4
    // requires monotonic DTS, so rollback handling is covered by the C++
    // unit tests rather than a container-level fixture.
    double shift_seconds = 0.0;
};

struct FixtureOptions {
    std::string output_path;
    int width = 160;
    int height = 120;
    int frame_rate_num = 30;
    int frame_rate_den = 1;
    int video_frames = 120;                 // 4 s at 30 fps
    int sample_rate = 44100;
    int audio_channels = 2;
    double video_start_seconds = 0.0;       // non-zero start timestamp
    double audio_start_seconds = 0.05;      // audio starts 50 ms later
    double audio_duration_seconds = 4.5;    // intentionally longer than video
    // Deterministic variable frame rate: per-frame extra duration pattern in
    // ticks of the video time base, repeated cyclically.
    std::vector<int> vfr_extra_ticks{0, 1, 0, 0, -1, 0};
    std::vector<FixtureHazard> hazards{
        {2.0, 0.45},  // forward gap beyond the discontinuity tolerance
        {3.1, 0.30},  // second, smaller forward gap
    };
    // Zero-based video frame indices whose packet PTS is deliberately removed
    // before muxing. Only honored by containers that can store missing
    // timestamps (e.g. Matroska); the MP4 muxer would regenerate them.
    std::vector<int> drop_pts_at_frames;
    std::uint32_t seed = 0x5157u;  // fixed LCG seed -> deterministic content
};

// Generates an MP4 with mpeg4 video and AAC audio entirely in-process.
// Returns true on success; on failure sets error_message.
bool generateFixture(const FixtureOptions& options, std::string& error_message);

}  // namespace pwsync
