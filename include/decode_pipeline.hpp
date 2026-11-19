#pragma once

#include <functional>
#include <string>
#include <vector>

#include "clock.hpp"
#include "media_types.hpp"

namespace pwsync {

// Normalized timestamps and frames are delivered through these callbacks so
// the engine stays independent from the simulator/report implementation.
using PacketCallback = std::function<void(const NormalizedPacket&)>;
using FrameCallback = std::function<void(const NormalizedFrame&, bool flushed)>;

struct DecodeConfig {
    double discontinuity_tolerance_seconds = 0.25;
    PipelineLimits limits{};
    // Callbacks may be empty if the caller only wants one of the streams.
    PacketCallback on_packet;
    FrameCallback on_frame;
};

struct DecodeResult {
    bool ok = false;
    int video_stream_index = -1;
    int audio_stream_index = -1;
    double duration_seconds = 0.0;
    double video_frame_rate = 0.0;  // average frame rate, 0 if unknown
    int audio_sample_rate = 0;
    int audio_channels = 0;
    int audio_frame_samples = 0;  // nominal samples per decoded audio frame
    std::uint64_t timestamp_repairs = 0;
    std::uint64_t discontinuities = 0;
    std::uint64_t decode_failures = 0;
    std::uint64_t flushed_frames = 0;
    std::vector<MediaError> errors;
};

// Opens the local container, finds audio/video streams, decodes every frame
// (including end-of-stream buffered frames) and normalizes timestamps.
// Returns ok=false with categorized errors; the result still carries the
// partial counters collected before the failure.
DecodeResult runDecodePipeline(const std::string& path, const DecodeConfig& config);

}  // namespace pwsync
