#pragma once

#include <string>

namespace fmp4 {

// Generates a local fixture tree under out_dir:
//   init.mp4            two tracks (90000/48000), trex defaults, stsd entries
//   init-bad.mp4        same but track 2 timescale is zero
//   segments-good/      seg-0041..0044.mp4, mixed tfdt/trun versions and
//                       duration sources (trun / tfhd default / trex default)
//   segments-missing/   same but without sequence 43
//   segments-delta/     same but with a decode-time jump at sequence 42
//   segments-corrupt/   one truncated segment
void write_fixture(const std::string& out_dir);

}  // namespace fmp4
