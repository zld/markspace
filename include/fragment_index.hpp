#pragma once

#include "box_reader.hpp"
#include "init_compat.hpp"

#include <cstdint>
#include <map>
#include <memory>
#include <string>
#include <vector>

namespace mp4flow {

struct MdatRange {
  uint64_t offset = 0; // absolute file offset of mdat payload
  uint64_t size = 0;   // mdat payload length
};

struct TrunSample {
  uint32_t duration = 0;
  uint32_t size = 0;
  int64_t compositionOffset = 0; // signed; version 1 sign-extended
};

struct TrackFragment {
  uint32_t trackId = 0;
  uint64_t decodeTime = 0;       // raw tfdt value in track timescale ticks
  bool tfdtPresent = false;
  uint32_t sampleCount = 0;
  uint64_t totalDuration = 0;    // sum of resolved sample durations
  std::vector<TrunSample> samples;
  int64_t dataOffset = 0;        // signed trun data_offset field
  bool haveTrunOffset = false;
  uint64_t resolvedDataOffset = 0; // absolute after base/offset resolution
  uint32_t defaultDuration = 0;  // tfhd default, else trex default
  uint32_t defaultSize = 0;
};

struct MoofIndex {
  uint32_t sequence = 0;         // mfhd sequence_number
  const Box* moof = nullptr;     // points into ParsedFile storage
  const Box* mdat = nullptr;
  MdatRange mdatRange;
  std::vector<TrackFragment> tracks;
  uint64_t moofEnd = 0;          // absolute offset just past moof box
};

struct FragmentFile {
  std::string path;
  int ordinal = 0;               // decimal prefix of file name
  std::shared_ptr<ParsedFile> parsed;
  std::vector<MoofIndex> moofs;
};

struct SeqConflict {
  uint32_t sequence;
  std::string fileA;
  std::string fileB;
};

struct MoofRef {
  size_t fileIndex = 0;
  size_t moofIndex = 0;
};

struct FragmentIndex {
  std::vector<FragmentFile> files;        // sorted by (ordinal, name)
  std::map<uint32_t, MoofRef> bySeq;      // first owner; duplicates in conflicts
  std::vector<SeqConflict> conflicts;

  const MoofIndex* find(uint32_t seq) const;
  std::vector<uint32_t> missingSequences(uint32_t first, uint32_t last) const;
};

// Build the index of every regular file in dir whose name begins with a
// decimal ordinal. `inits` supplies trex defaults for sample resolution.
FragmentIndex buildIndex(const std::string& dir,
                         const std::vector<InitSignature>& inits);

// Validate that every sample data range lies inside its paired mdat payload.
// Throws Mp4Error (BadValue/Truncated) with a box path on any violation.
void validateSampleRanges(const FragmentIndex& idx);

} // namespace mp4flow
