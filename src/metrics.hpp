#pragma once

#include <cstdint>
#include <mutex>
#include <string>
#include <vector>

namespace avsync {

// Percentile summary in milliseconds. `empty` is true when no samples were
// recorded; callers must render nulls instead of fabricated zeros.
struct StatsSummary {
    bool empty = true;
    int64_t count = 0;
    double min_ms = 0.0;
    double max_ms = 0.0;
    double mean_ms = 0.0;
    double p50_ms = 0.0;
    double p95_ms = 0.0;
    double p99_ms = 0.0;
};

// Thread-safe aggregation of sync events and time series.
class Metrics {
public:
    void add_av_offset_us(int64_t v);
    void add_drift_us(int64_t v);

    void inc_displayed() { bump(Counter::kDisplayed); }
    void inc_dropped() { bump(Counter::kDropped); }
    void inc_repeated() { bump(Counter::kRepeated); }
    void inc_underruns() { bump(Counter::kUnderruns); }
    void inc_missing_pts() { bump(Counter::kMissingPts); }
    void add_video_frames(int64_t n);
    void add_audio_samples(int64_t n);

    StatsSummary av_offset() const;
    StatsSummary drift() const;

    int64_t displayed() const { return get(Counter::kDisplayed); }
    int64_t dropped() const { return get(Counter::kDropped); }
    int64_t repeated() const { return get(Counter::kRepeated); }
    int64_t underruns() const { return get(Counter::kUnderruns); }
    int64_t missing_pts() const { return get(Counter::kMissingPts); }
    int64_t video_frames() const;
    int64_t audio_samples() const;

private:
    enum class Counter { kDisplayed, kDropped, kRepeated, kUnderruns,
                         kMissingPts, kCount };
    void bump(Counter c);
    int64_t get(Counter c) const;
    static StatsSummary summarize(const std::vector<int64_t>& us);

    mutable std::mutex mu_;
    std::vector<int64_t> av_offset_us_;
    std::vector<int64_t> drift_us_;
    int64_t counters_[static_cast<size_t>(Counter::kCount)] = {};
    int64_t video_frames_ = 0;
    int64_t audio_samples_ = 0;
};

// Render a StatsSummary as a JSON object; all fields null when empty.
std::string stats_to_json(const char* name, const StatsSummary& s);

}  // namespace avsync
