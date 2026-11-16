#include "avsync/metrics.hpp"

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <mutex>
#include <sstream>

namespace avsync {
namespace {
std::string format_double(double value) {
  char buffer[64];
  std::snprintf(buffer, sizeof(buffer), "%.3f", value);
  return std::string(buffer);
}
}  // namespace

Metrics::Metrics() = default;

void Metrics::record_av_delta(double delta_ms) {
  deltas_ms_.push_back(delta_ms);
}

void Metrics::record_drift(double drift_ms) { drifts_ms_.push_back(drift_ms); }
void Metrics::add_video_frame() { ++video_frames_; }
void Metrics::add_audio_samples(int samples) {
  if (samples > 0) audio_samples_ += static_cast<uint64_t>(samples);
}
void Metrics::add_video_drop() { ++drops_; }
void Metrics::add_video_repeat() { ++repeats_; }
void Metrics::add_audio_underrun() { ++underruns_; }
void Metrics::set_duration_seconds(double seconds) { duration_seconds_ = seconds; }

const std::vector<double>& Metrics::bucket_edges_ms() {
  static const std::vector<double> edges = {0, 5, 10, 20, 50, 100, 200};
  return edges;
}

double Metrics::percentile_sorted(const std::vector<double>& sorted,
                                  double p) {
  if (sorted.empty()) return 0.0;
  // Nearest-rank method, clamped to the valid index range.
  const size_t rank = static_cast<size_t>(
      std::ceil(p * static_cast<double>(sorted.size())));
  const size_t index = std::clamp(rank, size_t{1}, sorted.size()) - 1;
  return sorted[index];
}

MetricsReport Metrics::snapshot() const {
  std::vector<double> sorted = deltas_ms_;
  std::sort(sorted.begin(), sorted.end());

  MetricsReport report;
  report.av_delta_p50_ms = percentile_sorted(sorted, 0.50);
  report.av_delta_p95_ms = percentile_sorted(sorted, 0.95);
  if (!sorted.empty()) {
    report.av_delta_max_ms = std::fabs(sorted.back());
    if (std::fabs(sorted.front()) > report.av_delta_max_ms) {
      report.av_delta_max_ms = std::fabs(sorted.front());
    }
  }
  if (!drifts_ms_.empty()) {
    report.master_drift_ms =
        drifts_ms_.back();  // Final residual; bounded by the clock.
  }
  report.video_frames = video_frames_;
  report.audio_samples = audio_samples_;
  report.video_drop_count = drops_;
  report.video_repeat_count = repeats_;
  report.audio_underrun_count = underruns_;
  report.duration_seconds = duration_seconds_;
  return report;
}

std::vector<uint64_t> Metrics::histogram() const {
  const auto& edges = bucket_edges_ms();
  std::vector<uint64_t> counts(edges.size() + 1, 0);
  for (double delta : deltas_ms_) {
    const double magnitude = std::fabs(delta);
    auto it = std::upper_bound(edges.begin(), edges.end(), magnitude);
    const size_t index = static_cast<size_t>(it - edges.begin());
    ++counts[std::min(index, counts.size() - 1)];
  }
  return counts;
}

std::string Metrics::render_text() const {
  const MetricsReport r = snapshot();
  std::ostringstream out;
  out << "master_drift_ms=" << format_double(r.master_drift_ms) << '\n'
      << "av_delta_p50_ms=" << format_double(r.av_delta_p50_ms) << '\n'
      << "av_delta_p95_ms=" << format_double(r.av_delta_p95_ms) << '\n'
      << "av_delta_max_ms=" << format_double(r.av_delta_max_ms) << '\n'
      << "video_frames=" << r.video_frames << '\n'
      << "audio_samples=" << r.audio_samples << '\n'
      << "video_drop_count=" << r.video_drop_count << '\n'
      << "video_repeat_count=" << r.video_repeat_count << '\n';
  return out.str();
}

std::string Metrics::render_json(const std::string& input_path) const {
  const MetricsReport r = snapshot();
  const auto counts = histogram();
  const auto& edges = bucket_edges_ms();

  std::ostringstream out;
  out << "{\n"
      << "  \"input\": \"" << input_path << "\",\n"
      << "  \"duration_seconds\": " << format_double(r.duration_seconds)
      << ",\n"
      << "  \"master_drift_ms\": " << format_double(r.master_drift_ms)
      << ",\n"
      << "  \"av_delta_p50_ms\": " << format_double(r.av_delta_p50_ms)
      << ",\n"
      << "  \"av_delta_p95_ms\": " << format_double(r.av_delta_p95_ms)
      << ",\n"
      << "  \"av_delta_max_ms\": " << format_double(r.av_delta_max_ms)
      << ",\n"
      << "  \"video_frames\": " << r.video_frames << ",\n"
      << "  \"audio_samples\": " << r.audio_samples << ",\n"
      << "  \"video_drop_count\": " << r.video_drop_count << ",\n"
      << "  \"video_repeat_count\": " << r.video_repeat_count << ",\n"
      << "  \"audio_underrun_count\": " << r.audio_underrun_count << ",\n"
      << "  \"av_delta_histogram_ms\": [\n";
  for (size_t i = 0; i < counts.size(); ++i) {
    out << "    {\"bucket_index\": " << i << ", \"count\": " << counts[i]
        << (i + 1 == counts.size() ? "}\n" : "},\n");
  }
  out << "  ],\n"
      << "  \"bucket_edges_ms\": [";
  for (size_t i = 0; i < edges.size(); ++i) {
    out << edges[i] << (i + 1 == edges.size() ? "" : ", ");
  }
  out << "]\n}\n";
  return out.str();
}

}  // namespace avsync
