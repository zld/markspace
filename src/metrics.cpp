#include "metrics.hpp"

#include <algorithm>
#include <cstdio>

namespace avsync {

void Metrics::add_av_offset_us(int64_t v) {
    std::lock_guard<std::mutex> lk(mu_);
    av_offset_us_.push_back(v);
}

void Metrics::add_drift_us(int64_t v) {
    std::lock_guard<std::mutex> lk(mu_);
    drift_us_.push_back(v);
}

void Metrics::bump(Counter c) {
    std::lock_guard<std::mutex> lk(mu_);
    ++counters_[static_cast<size_t>(c)];
}

int64_t Metrics::get(Counter c) const {
    std::lock_guard<std::mutex> lk(mu_);
    return counters_[static_cast<size_t>(c)];
}

void Metrics::add_video_frames(int64_t n) {
    std::lock_guard<std::mutex> lk(mu_);
    video_frames_ += n;
}

void Metrics::add_audio_samples(int64_t n) {
    std::lock_guard<std::mutex> lk(mu_);
    audio_samples_ += n;
}

int64_t Metrics::video_frames() const {
    std::lock_guard<std::mutex> lk(mu_);
    return video_frames_;
}

int64_t Metrics::audio_samples() const {
    std::lock_guard<std::mutex> lk(mu_);
    return audio_samples_;
}

StatsSummary Metrics::av_offset() const {
    std::lock_guard<std::mutex> lk(mu_);
    return summarize(av_offset_us_);
}

StatsSummary Metrics::drift() const {
    std::lock_guard<std::mutex> lk(mu_);
    return summarize(drift_us_);
}

static double percentile_nearest_rank(const std::vector<int64_t>& sorted,
                                      double p) {
    // Nearest-rank: index = ceil(p/100 * n) - 1, clamped.
    const size_t n = sorted.size();
    size_t idx = static_cast<size_t>((p * n + 99.0) / 100.0);
    if (idx == 0) idx = 1;
    if (idx > n) idx = n;
    return static_cast<double>(sorted[idx - 1]) / 1000.0;
}

StatsSummary Metrics::summarize(const std::vector<int64_t>& us) {
    StatsSummary s;
    if (us.empty()) return s;
    std::vector<int64_t> sorted = us;
    std::sort(sorted.begin(), sorted.end());
    s.empty = false;
    s.count = static_cast<int64_t>(sorted.size());
    s.min_ms = static_cast<double>(sorted.front()) / 1000.0;
    s.max_ms = static_cast<double>(sorted.back()) / 1000.0;
    long double sum = 0.0;
    for (int64_t v : sorted) sum += v;
    s.mean_ms = static_cast<double>(sum / sorted.size()) / 1000.0;
    s.p50_ms = percentile_nearest_rank(sorted, 50.0);
    s.p95_ms = percentile_nearest_rank(sorted, 95.0);
    s.p99_ms = percentile_nearest_rank(sorted, 99.0);
    return s;
}

std::string stats_to_json(const char* name, const StatsSummary& s) {
    char buf[512];
    if (s.empty) {
        std::snprintf(buf, sizeof(buf),
                      "\"%s\":{\"min\":null,\"max\":null,\"mean\":null,"
                      "\"p50\":null,\"p95\":null,\"p99\":null,\"count\":0}",
                      name);
    } else {
        std::snprintf(buf, sizeof(buf),
                      "\"%s\":{\"min\":%.3f,\"max\":%.3f,\"mean\":%.3f,"
                      "\"p50\":%.3f,\"p95\":%.3f,\"p99\":%.3f,\"count\":%lld}",
                      name, s.min_ms, s.max_ms, s.mean_ms, s.p50_ms, s.p95_ms,
                      s.p99_ms, static_cast<long long>(s.count));
    }
    return buf;
}

}  // namespace avsync
