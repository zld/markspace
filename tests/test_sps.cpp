// Tests for the sps module.
#include <cstdio>
#include <string>
#include <vector>

#include "sps_fixture.hpp"
#include "sps_parser.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

bool parse_sps_fixture(const selftest::SpsFixture& fixture, Sps& sps,
                       Diagnostic& diag, ExpGolombStats* stats_out = nullptr,
                       std::size_t* payload_size_out = nullptr) {
    const std::vector<std::uint8_t> payload = selftest::escape_rbsp(
        selftest::build_sps_rbsp(fixture));
    if (payload_size_out) *payload_size_out = payload.size();
    ExpGolombStats stats;
    const bool ok = parse_sps(payload.data(), payload.size(), 5, 0, stats, sps, &diag);
    if (stats_out) *stats_out = stats;
    return ok;
}

void test_sps_baseline_frame_dimensions() {
    selftest::SpsFixture fixture;
    fixture.profile_idc = 66;  // Baseline: no chroma syntax
    fixture.level_idc = 30;
    fixture.id = 3;
    fixture.pic_width_in_mbs_minus1 = 79;         // 1280
    fixture.pic_height_in_map_units_minus1 = 44;  // 720

    Sps sps;
    Diagnostic diag;
    ExpGolombStats stats;
    CHECK(parse_sps_fixture(fixture, sps, diag, &stats));
    CHECK(diag.kind == ErrorKind::None);
    CHECK_EQ(sps.id, std::uint32_t{3});
    CHECK_EQ(sps.profile_idc, std::uint32_t{66});
    CHECK_EQ(sps.level_idc, std::uint32_t{30});
    CHECK_EQ(sps.chroma_format_idc, std::uint32_t{1});
    CHECK_EQ(sps.bit_depth_luma, std::uint32_t{8});
    CHECK_EQ(sps.bit_depth_chroma, std::uint32_t{8});
    CHECK_EQ(sps.log2_max_frame_num, std::uint32_t{4});
    CHECK_EQ(sps.log2_max_pic_order_cnt_lsb, std::uint32_t{6});
    CHECK_EQ(sps.width, std::uint32_t{1280});
    CHECK_EQ(sps.height, std::uint32_t{720});
    CHECK_EQ(sps.coded_width, std::uint32_t{1280});
    CHECK_EQ(sps.coded_height, std::uint32_t{720});
    CHECK(sps.frame_mbs_only_flag);
    CHECK(!sps.interlaced);
    CHECK(!sps.frame_cropping_flag);
    CHECK_EQ(sps.max_num_ref_frames, std::uint64_t{4});
    CHECK(stats.ue_calls > 0);
    CHECK_EQ(stats.ue_failures, std::uint64_t{0});
}

void test_sps_high_profile_crop_and_interlace() {
    selftest::SpsFixture fixture;
    fixture.profile_idc = 100;
    fixture.chroma_format_idc = 2;
    fixture.bit_depth_luma_minus8 = 2;
    fixture.bit_depth_chroma_minus8 = 2;
    fixture.scaling_matrix = true;
    fixture.frame_mbs_only = false;  // field-coded stream
    fixture.mb_adaptive_frame_field = true;
    fixture.crop = true;
    // 1920x1080 encoded as 1088 lines: bottom crop of 8 luma lines.
    fixture.pic_width_in_mbs_minus1 = 119;
    fixture.pic_height_in_map_units_minus1 = 33;
    fixture.crop_bottom = 4;

    Sps sps;
    Diagnostic diag;
    CHECK(parse_sps_fixture(fixture, sps, diag));
    CHECK_EQ(sps.chroma_format_idc, std::uint32_t{2});
    CHECK_EQ(sps.bit_depth_luma, std::uint32_t{10});
    CHECK_EQ(sps.bit_depth_chroma, std::uint32_t{10});
    CHECK_EQ(sps.coded_width, std::uint32_t{1920});
    CHECK_EQ(sps.coded_height, std::uint32_t{1088});
    CHECK_EQ(sps.height, std::uint32_t{1080});
    CHECK_EQ(sps.width, std::uint32_t{1920});
    CHECK(!sps.frame_mbs_only_flag);
    CHECK(sps.interlaced);
    CHECK(sps.mb_adaptive_frame_field_flag);
    CHECK(sps.seq_scaling_matrix_present);
}

void test_sps_chroma_crop_units() {
    // 4:2:2 has SubWidthC=2; crop_unit_x makes 8 units = 16 luma pixels.
    selftest::SpsFixture fixture;
    fixture.profile_idc = 122;
    fixture.chroma_format_idc = 2;
    fixture.crop = true;
    fixture.crop_right = 8;
    Sps sps;
    Diagnostic diag;
    CHECK(parse_sps_fixture(fixture, sps, diag));
    CHECK_EQ(sps.width, std::uint32_t{304});
}

void test_sps_rejects_impossible_crop() {
    selftest::SpsFixture fixture;
    fixture.crop = true;
    fixture.crop_left = 160;  // 320 luma px at 4:2:0 crop units of 2
    Sps sps;
    Diagnostic diag;
    CHECK(!parse_sps_fixture(fixture, sps, diag));
    CHECK(diag.kind == ErrorKind::IllegalValue);
    // Fields decoded before the failure remain available.
    CHECK_EQ(sps.coded_width, std::uint32_t{320});
    CHECK_EQ(sps.crop_left, std::uint32_t{160});
}

void test_sps_rejects_oversized_dimensions() {
    selftest::SpsFixture fixture;
    fixture.pic_width_in_mbs_minus1 = 4095;   // 65536 px
    Sps sps;
    Diagnostic diag;
    CHECK(!parse_sps_fixture(fixture, sps, diag));
    CHECK(diag.kind == ErrorKind::LimitExceeded);
}

void test_sps_truncated_payload_keeps_fields() {
    selftest::SpsFixture fixture;
    fixture.profile_idc = 66;
    fixture.id = 1;
    const std::vector<std::uint8_t> full = selftest::build_sps_rbsp(fixture);
    const std::vector<std::uint8_t> partial(full.begin(), full.begin() + 4);
    Sps sps;
    Diagnostic diag;
    ExpGolombStats stats;
    CHECK(!parse_sps(partial.data(), partial.size(), 5, 0, stats, sps, &diag));
    CHECK(diag.kind == ErrorKind::Truncated);
    CHECK_EQ(sps.profile_idc, std::uint32_t{66});
    CHECK_EQ(sps.id, std::uint32_t{1});
    CHECK(stats.ue_failures > 0);
}

void test_sps_unsupported_extension_is_reported() {
    selftest::SpsFixture fixture;
    fixture.profile_idc = 66;
    fixture.pic_order_cnt_type = 3;  // invalid per spec
    Sps sps;
    Diagnostic diag;
    CHECK(!parse_sps_fixture(fixture, sps, diag));
    CHECK(diag.kind == ErrorKind::Unsupported);
}

}  // namespace

REGISTER_TEST(test_sps_baseline_frame_dimensions);
REGISTER_TEST(test_sps_high_profile_crop_and_interlace);
REGISTER_TEST(test_sps_chroma_crop_units);
REGISTER_TEST(test_sps_rejects_impossible_crop);
REGISTER_TEST(test_sps_rejects_oversized_dimensions);
REGISTER_TEST(test_sps_truncated_payload_keeps_fields);
REGISTER_TEST(test_sps_unsupported_extension_is_reported);
