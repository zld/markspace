#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace pwsync {

// Exit codes required by the command line contract.
enum class ExitCode : int {
    Ok = 0,
    Generic = 1,
    Usage = 2,        // bad arguments or file path errors
    MediaError = 3,   // demux / decode / structural media failures
    ReportError = 4,  // report serialization or output failure
};

// Rational time base: one tick lasts num/den seconds. Mirrors AVRational
// semantics but keeps the FFmpeg headers out of the core data model.
struct StreamTimeBase {
    int num = 0;
    int den = 1;

    double secondsPerTick() const {
        return (den == 0 || num == 0) ? 0.0
                                      : static_cast<double>(num) / static_cast<double>(den);
    }
};

enum class MediaType : std::uint8_t {
    Other = 0,
    Video = 1,
    Audio = 2,
};

const char* toString(MediaType type);

// Packet metadata after the demux stage has normalized timestamps to
// seconds. The original time base and raw ticks are retained for diagnostics.
struct NormalizedPacket {
    MediaType type = MediaType::Other;
    int stream_index = -1;
    bool keyframe = false;
    bool corrupt = false;        // AV_PKT_FLAG_CORRUPT or demuxer error on this packet
    bool pts_repaired = false;   // PTS was missing and had to be inferred
    bool discontinuity = false;  // PTS jumped outside the tolerance window
    double pts_seconds = 0.0;
    double duration_seconds = 0.0;
    StreamTimeBase original_time_base{};
    std::int64_t original_pts = 0;
    bool original_pts_valid = false;
};

// Decoded frame with a presentation timestamp in seconds. Audio frames have
// been resampled to a fixed common format described by the audio fields.
struct NormalizedFrame {
    MediaType type = MediaType::Other;
    int stream_index = -1;
    bool pts_repaired = false;
    bool discontinuity = false;
    double pts_seconds = 0.0;
    double duration_seconds = 0.0;

    // Video geometry (0 for audio frames).
    int width = 0;
    int height = 0;
    bool keyframe = false;

    // Audio description (zero for video frames).
    int sample_rate = 0;
    int channels = 0;
    int nb_samples = 0;
};

// Fixed histogram edges in milliseconds. Bins are half-open:
//   bin 0:                 x < edges[0]
//   bin k (1..N-1):        edges[k-1] <= x < edges[k]
//   bin N:                 x >= edges[N-1]
// Stable across runs and inputs so build regressions can diff counts.
inline constexpr std::array<double, 9> kHistogramEdgesMs{
    -80.0, -40.0, -20.0, -10.0, 0.0, 10.0, 20.0, 40.0, 80.0};
inline constexpr std::size_t kHistogramBinCount = kHistogramEdgesMs.size() + 1;

struct OffsetStats {
    std::uint64_t samples = 0;
    double p50_ms = 0.0;
    double p95_ms = 0.0;
    double max_abs_ms = 0.0;
    double mean_ms = 0.0;
    std::array<std::uint64_t, kHistogramBinCount> histogram{};
};

struct PathStats {
    std::string name;
    double delay_ms = 0.0;
    OffsetStats av_offset{};
    std::uint64_t video_frames = 0;
    std::uint64_t audio_frames = 0;
    std::uint64_t dropped_frames = 0;
    std::uint64_t repeated_frames = 0;
    std::uint64_t waits = 0;
};

struct ClockStats {
    std::string mode;  // "audio" or "monotonic"
    std::uint64_t samples = 0;
    std::uint64_t resets = 0;
    double cumulative_drift_ms = 0.0;  // signed sum of per-step drift
    double mean_drift_ms = 0.0;
    double max_abs_drift_ms = 0.0;
};

enum class ErrorCategory : std::uint8_t {
    OpenFailed,
    MissingStream,
    CorruptPacket,
    DecodeError,
    LimitExceeded,
    WriteFailed,
    BadParameter,
};

const char* toString(ErrorCategory category);

struct MediaError {
    ErrorCategory category = ErrorCategory::DecodeError;
    int stream_index = -1;
    std::string message;
};

// Hard safety limits applied while decoding local files.
struct PipelineLimits {
    double max_duration_seconds = 3600.0;  // one hour of media
    std::uint64_t max_decoded_frames = 200000;
    int max_frame_width = 7680;
    int max_frame_height = 4320;
};

struct SyncReport {
    std::string input_file;
    std::string clock_mode;
    double late_threshold_ms = 0.0;
    double discontinuity_tolerance_ms = 0.0;
    double media_duration_seconds = 0.0;

    ClockStats clock{};
    PathStats path_a{};
    PathStats path_b{};

    std::uint64_t timestamp_repairs = 0;
    std::uint64_t discontinuities = 0;
    std::uint64_t corrupted_packets = 0;
    std::uint64_t decode_failures = 0;
    std::uint64_t video_packets = 0;
    std::uint64_t audio_packets = 0;
    std::uint64_t video_frames_decoded = 0;
    std::uint64_t audio_frames_decoded = 0;
    std::uint64_t flushed_frames = 0;  // frames emitted only during end-of-stream drain

    std::vector<MediaError> errors;
};

}  // namespace pwsync
