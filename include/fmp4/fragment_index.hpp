#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

#include "fmp4/box.hpp"

namespace fmp4 {

// trex defaults from the init segment, also used as the last fallback when
// resolving sample durations of a fragment.
struct TrexDefaults {
  uint32_t track_id = 0;
  uint32_t default_sample_description_index = 0;
  uint32_t default_sample_duration = 0;
  uint32_t default_sample_size = 0;
  uint32_t default_sample_flags = 0;
};

struct Tfhd {
  uint32_t track_id = 0;
  std::optional<uint64_t> base_data_offset;
  std::optional<uint32_t> sample_description_index;
  std::optional<uint32_t> default_sample_duration;
  std::optional<uint32_t> default_sample_size;
  std::optional<uint32_t> default_sample_flags;
  bool default_base_is_moof = false;
  uint64_t box_offset = 0;
  // Absolute file offset of the base_data_offset field (valid when set).
  uint64_t base_data_offset_field = 0;
};

struct Tfdt {
  uint32_t version = 0;
  uint64_t value = 0;  // base media decode time, 32-bit (v0) or 64-bit (v1)
};

struct TrunSample {
  std::optional<uint32_t> duration;
  std::optional<uint32_t> size;
  std::optional<uint32_t> flags;
  std::optional<int64_t> cto;  // unsigned for trun v0, signed for trun v1
};

struct Trun {
  uint32_t version = 0;
  uint32_t flags = 0;
  std::optional<int32_t> data_offset;
  std::optional<uint32_t> first_sample_flags;
  std::vector<TrunSample> samples;
  uint64_t box_offset = 0;
  // Absolute file offset of the data_offset field (valid when set).
  uint64_t data_offset_field = 0;
};

struct Traf {
  Tfhd tfhd;
  std::optional<Tfdt> tfdt;
  std::vector<Trun> truns;
};

// One moof+mdat pair. mdat must immediately follow the moof.
struct Fragment {
  uint32_t sequence = 0;  // mfhd sequence number
  uint64_t moof_offset = 0;
  uint64_t moof_size = 0;
  uint64_t mdat_offset = 0;
  uint64_t mdat_size = 0;
  uint64_t mdat_payload_offset = 0;
  std::vector<Traf> trafs;
  std::string file;
};

struct SegmentIndex {
  std::string file;
  std::vector<Fragment> fragments;
};

// Parses a media segment file and indexes every moof+mdat pair by its mfhd
// sequence number. Unknown box versions/flags and structural corruption throw
// BoxError (exit code kExitFormat).
SegmentIndex build_segment_index(const std::string& path);

// Sample duration resolution order: trun entry, then tfhd default, then trex
// default. Throws BoxError when no usable default exists.
uint32_t resolve_sample_duration(const Traf& traf, const TrunSample& sample,
                                 const TrexDefaults* trex,
                                 const std::string& file, uint64_t offset);

// Total decode duration of all samples in a traf, in track timescale ticks.
uint64_t traf_duration(const Traf& traf, const TrexDefaults* trex,
                       const std::string& file);

}  // namespace fmp4
