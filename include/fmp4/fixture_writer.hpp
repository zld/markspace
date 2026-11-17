#pragma once
// Fixture writer: synthesizes a two-track fragmented MP4 dataset entirely
// locally. Used both by the CLI and by tests; no external media is referenced.
#include <cstdint>
#include <string>
#include <vector>

namespace fmp4::fixture {

struct Options {
  std::string dir;
  uint32_t segments = 4;
  bool mixed_versions = false;   // alternate tfdt/trun version fields
  bool omit_incompatible = false; // unused placeholder kept explicit
  // manifest sequence (1-based) whose fragment file is not written.
  uint32_t omit_file_for_seq = 0;
  // manifest sequence whose mfhd gets a skipped number.
  uint32_t mfhd_gap_at = 0;
  // manifest sequence whose first box length is deliberately corrupt.
  uint32_t corrupt_length_at = 0;
  // Emit an init segment whose video sample entry differs (avc1 -> encv).
  bool incompatible_init = false;
};

// Writes init.mp4, fragments.manifest and the fragment files. Returns the
// actual mfhd sequence numbers in manifest order; omitted files are skipped.
std::vector<uint32_t> write_fixture(const Options &opt);

} // namespace fmp4::fixture
