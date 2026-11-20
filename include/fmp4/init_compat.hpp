#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "fmp4/fragment_index.hpp"

namespace fmp4 {

struct InitTrack {
  uint32_t track_id = 0;
  uint32_t timescale = 0;
  uint32_t stsd_entry_count = 0;
  bool has_trex = false;
  TrexDefaults trex;
};

struct InitInfo {
  std::string file;
  std::vector<InitTrack> tracks;
  const InitTrack* find_track(uint32_t track_id) const {
    for (const auto& t : tracks)
      if (t.track_id == track_id) return &t;
    return nullptr;
  }
};

// Parses an init segment: moov/trak (tkhd track_ID, mdhd timescale, stsd
// sample description entries) and mvex/trex defaults.
InitInfo parse_init(const std::string& path);

struct InitIssue {
  uint32_t track_id = 0;
  std::string field;
  std::string message;
};

// Validates the init segment itself: nonzero timescale, trex present, stsd
// entries present, trex sample description index within stsd range.
std::vector<InitIssue> validate_init(const InitInfo& init);

// Validates fragments against the init segment: every traf track_ID must
// exist in the init segment and tfhd sample_description_index must reference
// an existing stsd entry.
std::vector<InitIssue> validate_fragments(const InitInfo& init,
                                          const std::vector<Fragment>& frags);

}  // namespace fmp4
