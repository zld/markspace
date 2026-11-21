#pragma once

#include "fragment_index.hpp"
#include "init_compat.hpp"

#include <cstdint>
#include <map>
#include <string>
#include <vector>

namespace mp4flow {

struct TrackRebaseStat {
  uint32_t trackId = 0;
  uint32_t timescale = 0;
  int64_t maxAbsDecodeDeltaTicks = 0;
};

struct RepackReport {
  size_t inputFragments = 0;  // source files
  size_t inputMoofs = 0;
  size_t outputFragments = 0; // one output file per moof
  size_t outputMoofs = 0;
  uint32_t firstOutSeq = 0;
  uint32_t lastOutSeq = 0;
  bool continuous = false;
  std::vector<TrackRebaseStat> tracks;
  std::vector<std::string> outputFiles;
};

struct RepackOptions {
  std::string initDir;
  std::string mediaDir;
  std::string outDir;
  uint64_t maxCorrectionMs = 0; // 0 disables the limit
};

// Rebuild every moof as its own output fragment under outDir. Files are
// written into a sibling staging directory and renamed into place only after
// a full re-parse validation; staging is always cleaned up on failure.
RepackReport repack(const RepackOptions& opt,
                    const std::vector<InitSignature>& inits,
                    const FragmentIndex& idx);

} // namespace mp4flow
