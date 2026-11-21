#pragma once

#include "fragment_index.hpp"
#include "init_compat.hpp"

#include <cstdint>
#include <string>
#include <vector>

namespace mp4flow {

struct VerifyOptions {
  std::string initDir;
  std::string mediaDir;
  bool hasExpectedRange = false;
  uint32_t expectFirst = 0;
  uint32_t expectLast = 0;
};

struct VerifyReport {
  bool ok = false;
  size_t fragmentFiles = 0;
  size_t moofCount = 0;
  bool sampleRangesValid = false;
  std::vector<uint32_t> missing;      // missing expected moof sequences
  std::vector<SeqConflict> duplicates;
  std::vector<SeqConflict> ordinalConflicts;
};

// Re-runs the full parse/index pipeline over mediaDir and checks layout,
// sample ranges and (optionally) the expected inclusive sequence range.
VerifyReport verifyMedia(const VerifyOptions& opt,
                         const std::vector<InitSignature>& inits);

} // namespace mp4flow
