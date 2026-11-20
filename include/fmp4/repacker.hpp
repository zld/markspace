#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "fmp4/init_compat.hpp"

namespace fmp4 {

struct RepackOptions {
  std::string init_path;
  std::string segments_dir;
  std::string out_dir;
  uint32_t first_seq = 0;
  uint32_t last_seq = 0;
  int64_t max_decode_delta_ticks = 0;
  bool json_stdout = false;
  std::string report_path;  // optional JSON report file
};

// Per-track signed decode-time deltas, one entry per fragment boundary.
struct TrackDeltas {
  uint32_t track_id = 0;
  std::vector<int64_t> decode_delta_ticks;
  std::vector<uint32_t> next_seq;  // sequence of the later fragment, parallel
};

struct RepackSummary {
  uint64_t before_fragments = 0;
  uint64_t after_fragments = 0;
  bool before_moof_contiguous = false;
  bool after_moof_contiguous = false;
  std::vector<TrackDeltas> before_deltas;
  std::vector<TrackDeltas> after_deltas;
  std::vector<uint32_t> missing;
  std::vector<uint32_t> duplicates;
  std::vector<uint32_t> out_of_order;
  std::string output_file;
};

// For every pair of adjacent fragments (sorted by sequence) and every track
// present in both: delta = next.tfdt - (prev.tfdt + prev sample durations).
// frags must be sorted by sequence number.
std::vector<TrackDeltas> compute_decode_deltas(
    const std::vector<Fragment>& frags, const InitInfo& init);

// Runs the full repack pipeline and returns a process exit code (kExit*).
// Errors are reported on stderr; MISSING_MOOF_SEQ=/DUPLICATE_MOOF_SEQ= lines
// go to stdout. On success writes the JSON report to stdout (when json_stdout)
// and/or to report_path.
int run_repack(const RepackOptions& opt);

}  // namespace fmp4
