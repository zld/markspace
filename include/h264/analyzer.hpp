#pragma once

#include <cstddef>
#include <string>
#include <vector>

#include "h264/au_assembler.hpp"
#include "h264/gop_index.hpp"
#include "h264/nal.hpp"
#include "h264/stats.hpp"
#include "h264/syntax.hpp"

namespace h264 {

enum class AnalyzeMode {
  kStrict,    // first error stops the run
  kRecovery,  // skip damaged NALs and mark the report incomplete
};

struct AnalysisOptions {
  AnalyzeMode mode = AnalyzeMode::kStrict;
  std::size_t max_nal_size = 64u * 1024u * 1024u;
};

struct AnalysisReport {
  bool ok = false;
  bool incomplete = false;

  AnnexBScan scan;
  ParameterSets sets;
  std::vector<Picture> pictures;
  std::vector<Picture> dropped_pictures;
  GopIndex gops;
  StreamStats stats;
  std::vector<std::string> warnings;
  std::vector<Diagnostic> errors;

  std::size_t leading_zero_bytes = 0;
  std::size_t empty_nal_count = 0;
  bool had_truncated_start_code = false;
  int unknown_nal_type_count = 0;
};

// Full pipeline: NAL scan -> parameter sets -> slice headers -> pictures ->
// GOP index -> statistics. In strict mode the first error is rethrown;
// in recovery mode damaged NALs are skipped and the report is flagged
// incomplete.
AnalysisReport analyze_stream(const std::vector<std::uint8_t>& data,
                              const AnalysisOptions& options);

std::string format_text_report(const AnalysisReport& report,
                               const std::string& input_name);
std::string format_json_report(const AnalysisReport& report,
                               const std::string& input_name);

}  // namespace h264
