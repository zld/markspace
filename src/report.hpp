#pragma once

#include <string>

#include "histogram.hpp"
#include "options.hpp"
#include "render_paths.hpp"

namespace lutcheck {

struct Analysis {
  std::uint64_t pixels = 0;
  int grid_size = 0;
  std::string input_space;
  std::string lut_space;
  int histogram_bins = 0;
  int threads = 0;       // requested by the user
  int workers = 0;       // actually started, bounded by chunk count
  Deviation deviation;
  Histogram before;
  Histogram after;
  std::vector<std::string> paths;
  double threshold = 0.0;
  bool deviation_exceeded = false;
  // The image the after-histogram was computed from; also what `--output`
  // writes, so the file and the report can never disagree.
  Image rendered;
};

// Runs the full in-memory pipeline: color conversion, requested render paths,
// path comparison and histograms. Exposed so tests can drive it without the
// filesystem.
Analysis run_analysis(const Image& input, const Lut3D& lut,
                      const Options& options);

// Stable field order and fixed formatting; every floating point value is
// checked to be finite before serialization.
std::string analysis_json(const Analysis& analysis, bool pretty);

// Human readable summary used when --json is not requested.
std::string analysis_text(const Analysis& analysis);

// Escapes a string for JSON, including quote and backslash characters.
std::string json_quote(const std::string& text);

// Maps a completed analysis to its process exit code: ExitCode::Deviation
// only when a path comparison ran and exceeded the threshold.
int report_exit_code(const Analysis& analysis);

}  // namespace lutcheck
