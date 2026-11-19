#pragma once

#include <cstdint>

#include "media_types.hpp"

namespace pwsync {

enum class ClockMode : std::uint8_t {
    AudioMaster = 0,
    Monotonic = 1,
};

ClockMode parseClockMode(const std::string& text);  // throws std::invalid_argument

// Per-stream timestamp normalizer. Converts raw ticks to seconds, infers
// missing PTS from the previous frame duration, and flags discontinuities
// (PTS rollback or large jumps). One instance per stream; stateful.
class TimestampNormalizer {
public:
    // fallback_duration_seconds: used when packets carry no duration and as
    // the nominal frame period for discontinuity classification.
    TimestampNormalizer(double discontinuity_tolerance_seconds,
                        double fallback_duration_seconds);

    struct Result {
        double pts_seconds = 0.0;
        double duration_seconds = 0.0;
        bool repaired = false;
        bool discontinuity = false;
    };

    // duration_ticks may be 0 if the demuxer provides no packet duration.
    Result normalize(std::int64_t pts, bool pts_valid, std::int64_t duration_ticks,
                     const StreamTimeBase& time_base);

    std::uint64_t repairs() const { return repairs_; }
    std::uint64_t discontinuities() const { return discontinuities_; }
    bool initialized() const { return initialized_; }
    double lastDurationSeconds() const { return last_duration_seconds_; }
    double lastPtsSeconds() const { return last_pts_seconds_; }

private:
    double tolerance_seconds_;
    double fallback_duration_seconds_;
    bool initialized_ = false;
    double last_pts_seconds_ = 0.0;
    double last_duration_seconds_ = 0.0;
    std::uint64_t repairs_ = 0;
    std::uint64_t discontinuities_ = 0;
};

// Self-contained master clock. Two modes:
//   AudioMaster: driven by audio frame PTS (wall-clock semantics)
//   Monotonic:   driven by video frames, always non-decreasing
//
// The clock keeps two timelines: the actual media PTS timeline and a nominal
// timeline advanced by a fixed per-frame step. Their difference is the drift.
// Discontinuities re-anchor both timelines, so a negative duration is never
// accumulated into the drift.
class MasterClock {
public:
    MasterClock(ClockMode mode);

    // Feed one reference-frame event. nominal_step_seconds is the expected
    // frame duration (constant: average frame rate for video, codec frame
    // size / sample rate for audio). Returns the signed drift in seconds
    // measured after advancing (0.0 for the anchoring frame).
    double updateReference(double pts_seconds, double nominal_step_seconds,
                           bool discontinuity);

    // Current master time in seconds (actual PTS timeline, clamped monotonic).
    double nowSeconds() const { return actual_seconds_; }
    bool hasReference() const { return anchored_; }
    ClockMode mode() const { return mode_; }
    std::uint64_t resetCount() const { return resets_; }

    void reset();
    ClockStats stats() const;

private:
    void recordDrift(double drift_seconds);

    ClockMode mode_;
    bool anchored_ = false;
    double actual_seconds_ = 0.0;
    double nominal_seconds_ = 0.0;
    std::uint64_t samples_ = 0;
    std::uint64_t resets_ = 0;
    double drift_sum_ms_ = 0.0;
    double max_abs_drift_ms_ = 0.0;
};

}  // namespace pwsync
