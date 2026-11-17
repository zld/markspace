#pragma once
// Fragment index: manifest-driven view of an init segment plus every fragment.
// The index is the single structure shared by the validator and repacker.
#include <cstdint>
#include <string>
#include <vector>

#include "fmp4/box_reader.hpp"

namespace fmp4 {

struct TrackInit {
  uint32_t track_id = 0;
  uint32_t timescale = 0;
  uint32_t handler = 0;       // 'vide' / 'soun' / ...
  uint32_t sample_entry = 0;  // fourcc from stsd, e.g. avc1, mp4a
  uint32_t default_sample_desc_idx = 1;
  uint32_t default_sample_duration = 0;
  uint32_t default_sample_size = 0;
  uint32_t default_sample_flags = 0;
};

struct InitInfo {
  std::string path;
  std::vector<TrackInit> tracks;
};

struct TrackRun {
  uint32_t track_id = 0;
  uint64_t decode_start = 0; // tfdt baseMediaDecodeTime, ticks
  uint64_t duration = 0;     // summed sample durations, ticks
  uint32_t sample_count = 0;
};

struct FragmentInfo {
  uint32_t manifest_seq = 0; // expected sequence from fragments.manifest
  std::string rel_path;
  std::string abs_path;
  bool present = false;
  uint32_t mfhd_sequence = 0;
  uint64_t file_size = 0;
  uint64_t moof_offset = 0;
  uint64_t mfhd_offset = 0;     // absolute offset of mfhd size field
  uint32_t mfhd_header_size = 0;
  std::vector<TrackRun> tracks;
};

struct FragmentIndex {
  std::string root_dir;             // canonical
  InitInfo init;
  std::vector<FragmentInfo> fragments; // manifest order
  std::vector<uint32_t> missing_sequences;
  std::vector<uint32_t> duplicate_sequences;

  const FragmentInfo *find_manifest(uint32_t seq) const;
  const TrackInit *find_track(uint32_t track_id) const;
};

// Reads <dir>/fragments.manifest, the init segment and every present fragment.
// Missing fragment files are recorded in the index; corrupt boxes throw.
FragmentIndex build_index(const std::string &dir);

// Parse an init segment independently (verify --baseline).
InitInfo parse_init_segment(const std::string &abs_path);

// Parse one fragment file against init defaults. File-derived fields are
// filled; manifest_seq/rel_path are assigned later by build_index. Throws on
// corrupt boxes, unknown tracks or malformed tfhd/tfdt/trun.
FragmentInfo parse_fragment_file(const std::string &abs_path,
                                 const InitInfo &init);

std::string manifest_path(const std::string &dir);

} // namespace fmp4
