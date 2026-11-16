#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace avsync {

// Aggregated pipeline statistics. All timing fields are milliseconds; all
// counters are non-negative integers.
struct MetricsReport {
  double master_drift_ms = 0.0;
  double av_delta_p50_ms = 0.0;
  double av_delta_p95_ms = 0.0;
  double av_delta_max_ms = 0.0;
  uint64_t video_frames = 0;
  uint64_t audio_samples = 0;
  uint64_t video_drop_count = 0;
  uint64_t video_repeat_count = 0;
  uint64_t audio_underrun_count = 0;
  double duration_seconds = 0.0;
};

// Thread-safe-ish collector: output paths call record_* from the single
// scheduler thread, so internal locking only guards against late diagnostics.
class Metrics {
 public:
  Metrics();

  // Record one observed audio/video presentation delta, in milliseconds.
  void record_av_delta(double delta_ms);
  void record_drift(double drift_ms);
  void add_video_frame();
  void add_audio_samples(int samples);
  void add_video_drop();
  void add_video_repeat();
  void add_audio_underrun();
  void set_duration_seconds(double seconds);

  MetricsReport snapshot() const;

  // Fixed logarithmic-ish buckets: [-inf,0), [0,5), [5,10), ..., [200,+inf).
  static const std::vector<double>& bucket_edges_ms();
  // Counts per bucket; size is bucket_edges_ms().size() + 1.
  std::vector<uint64_t> histogram() const;

  // Terminal output: key=value lines matching the contracted metric names.
  std::string render_text() const;
  // Full report including the bucket distribution and run metadata.
  std::string render_json(const std::string& input_path) const;

 private:
  static double percentile_sorted(const std::vector<double>& sorted, double p);

  std::vector<double> deltas_ms_;
  std::vector<double> drifts_ms_;
  uint64_t video_frames_ = 0;
  uint64_t audio_samples_ = 0;
  uint64_t drops_ = 0;
  uint64_t repeats_ = 0;
  uint64_t underruns_ = 0;
  double duration_seconds_ = 0.0;
};

}  // namespace avsync
