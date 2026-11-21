#pragma once

#include "box_reader.hpp"

#include <string>
#include <vector>

namespace mp4flow {

struct SampleEntrySig {
  uint32_t entryType = 0; // e.g. 'avc1', 'mp4a'
  Bytes payload;          // exact sample entry payload (header excluded)
};

struct TrackSignature {
  uint32_t trackId = 0;
  std::string handlerType; // 'vide' / 'soun' / 'hint' ...
  uint32_t timescale = 0;
  SampleEntrySig sampleEntry;
  // trex defaults
  uint32_t trexDescIndex = 0;
  uint32_t trexDefDuration = 0;
  uint32_t trexDefSize = 0;
  uint32_t trexDefFlags = 0;
};

struct InitSignature {
  std::string path;
  std::vector<TrackSignature> tracks;
  // Encrypted sample entries found (file path + box path pairs flattened).
  std::vector<std::string> encryptedBoxes;
};

// Extract the signature of an initialization segment. Throws Mp4Error on
// malformed boxes; encrypted sample entries are recorded, not thrown.
InitSignature extractInit(const ParsedFile& pf);

struct FieldDiff {
  uint32_t trackId = 0;
  std::string field;
  std::string valueA;
  std::string valueB;
};

struct InitCompatReport {
  bool compatible = true;
  std::vector<FieldDiff> diffs;
  std::vector<std::string> encryptedRejections; // "file :: box path"
};

// Compare every signature pairwise against the first one.
InitCompatReport compareInits(const std::vector<InitSignature>& inits);

bool isEncryptedEntryType(uint32_t t);

} // namespace mp4flow
