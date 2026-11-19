#include <catch2/catch_test_macros.hpp>

#include "bit_writer.hpp"
#include "diagnostics.hpp"
#include "fixture_builder.hpp"
#include "nal_parser.hpp"
#include "parameter_sets.hpp"

using namespace h264;

namespace {

NalUnit wrap_nal(std::vector<std::uint8_t> rbsp, unsigned type,
                 std::uint64_t offset = 16) {
    NalUnit nal;
    nal.nal_offset = offset;
    nal.start_offset = offset - 4;
    nal.nal_type = type;
    nal.valid = true;
    // Builders emit header-less RBSP; prepend the NAL header byte.
    const std::uint8_t header =
        fixture::nal_header(type == NAL_PPS ? 3 : 3, type);
    rbsp.insert(rbsp.begin(), header);
    nal.payload = escape_ebsp(std::move(rbsp));
    return nal;
}

}  // namespace

TEST_CASE("baseline SPS yields profile, dimensions and frame-only flag",
          "[sps]") {
    Diagnostics diag;
    NalUnit nal = wrap_nal(fixture::build_sps_baseline(), NAL_SPS);
    SpsInfo sps;
    REQUIRE(parse_sps(nal, sps, diag));
    CHECK_FALSE(diag.has_errors());
    CHECK(sps.profile_idc == 66);
    CHECK(sps.level_idc == 30);
    CHECK(sps.chroma_format_idc == 1);
    CHECK(sps.bit_depth_luma_minus8 == 0);
    CHECK(sps.width == 320);
    CHECK(sps.height == 240);
    CHECK(sps.frame_mbs_only_flag);
}

TEST_CASE("high profile SPS parses crop-adjusted dimensions", "[sps]") {
    Diagnostics diag;
    NalUnit nal = wrap_nal(fixture::build_sps_high(), NAL_SPS);
    SpsInfo sps;
    REQUIRE(parse_sps(nal, sps, diag));
    CHECK(sps.profile_idc == 100);
    CHECK(sps.chroma_format_idc == 1);
    // 640 minus (1+1)*2 crop units -> 636, height unchanged at 480.
    CHECK(sps.width == 636);
    CHECK(sps.height == 480);
    CHECK(sps.frame_cropping_flag);
}

TEST_CASE("interlaced SPS reports field coding and doubled map height",
          "[sps]") {
    Diagnostics diag;
    NalUnit nal = wrap_nal(fixture::build_sps_interlaced(), NAL_SPS);
    SpsInfo sps;
    REQUIRE(parse_sps(nal, sps, diag));
    CHECK_FALSE(sps.frame_mbs_only_flag);
    CHECK(sps.mb_adaptive_frame_field_flag);
    CHECK(sps.width == 352);
    CHECK(sps.height == 288);
    CHECK(sps.log2_max_frame_num_minus4 == 2);
}

TEST_CASE("truncated SPS fails and keeps the NAL offset", "[sps]") {
    Diagnostics diag;
    // Header-less RBSP fragment; wrap_nal adds the NAL header.
    NalUnit nal = wrap_nal({0x42}, NAL_SPS, /*offset=*/99);
    SpsInfo sps;
    REQUIRE_FALSE(parse_sps(nal, sps, diag));
    REQUIRE(diag.has_errors());
    CHECK(diag[diag.size() - 1].offset == 99);
}

TEST_CASE("crop larger than the picture is rejected", "[sps]") {
    BitWriter w;
    w.write_bits(66, 8); w.write_bits(0, 8); w.write_bits(30, 8);
    w.write_ue(0); w.write_ue(0); w.write_ue(0); w.write_ue(0);
    w.write_ue(1); w.write_bit(0);
    w.write_ue(0); w.write_ue(0);       // 16x16 picture
    w.write_bit(1); w.write_bit(1);
    w.write_bit(1);                     // cropping present
    w.write_ue(50); w.write_ue(50); w.write_ue(0); w.write_ue(0);
    w.rbsp_trailing_bits();
    auto rbsp = w.take();

    Diagnostics diag;
    NalUnit nal = wrap_nal(rbsp, NAL_SPS, /*offset=*/5);
    SpsInfo sps;
    REQUIRE_FALSE(parse_sps(nal, sps, diag));
    CHECK(diag[diag.size() - 1].offset == 5);
}

TEST_CASE("PPS resolves to its SPS id and is cached", "[pps]") {
    Diagnostics diag;
    NalUnit pps_nal = wrap_nal(fixture::build_pps(3, 1, true), NAL_PPS);
    PpsInfo pps;
    REQUIRE(parse_pps(pps_nal, pps, diag));
    CHECK(pps.pps_id == 3);
    CHECK(pps.sps_id == 1);
    CHECK(pps.entropy_coding_mode_flag);

    ParameterSetStore store;
    store.add_pps(pps);
    REQUIRE(store.find_pps(3) != nullptr);
    CHECK(store.find_pps(99) == nullptr);
}
