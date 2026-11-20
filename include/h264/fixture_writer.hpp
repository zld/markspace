#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace h264 {

enum class FixtureProfile {
  kBaseline,           // progressive, baseline profile, IDR + P
  kInterlacedMultislice,  // the demo profile with fields and B pictures
};

struct FixtureSpec {
  int profile_idc = 66;
  int level_idc = 30;
  int pic_width_in_mbs = 4;
  int pic_height_in_map_units = 2;
  bool frame_mbs_only = false;
  int log2_max_frame_num = 4;
  int log2_max_pic_order_cnt_lsb = 6;
};

struct FixtureOptions {
  FixtureProfile profile = FixtureProfile::kInterlacedMultislice;
  bool use_four_byte_start_codes = true;
  std::size_t leading_zero_bytes = 0;
};

// Builds a deterministic Annex-B stream. The output contains no external
// assets and is byte-for-byte reproducible for a given option set.
std::vector<std::uint8_t> build_fixture(const FixtureOptions& options);

FixtureSpec fixture_spec(FixtureProfile profile);
std::string fixture_profile_name(FixtureProfile profile);
bool parse_fixture_profile(const std::string& name, FixtureProfile* out);

// Appends one Annex-B NAL unit (start code + header + escaped payload).
void append_nal(std::vector<std::uint8_t>* out, int nal_ref_idc,
                int nal_unit_type, const std::vector<std::uint8_t>& rbsp,
                bool four_byte_start_code);

}  // namespace h264
