#include <catch2/catch_test_macros.hpp>

#include "access_unit.hpp"
#include "diagnostics.hpp"
#include "fixture_builder.hpp"
#include "nal_parser.hpp"
#include "parameter_sets.hpp"

using namespace h264;

namespace {

// Builds a VCL NAL from slice syntax, assigning deterministic file offsets.
NalUnit make_vcl(bool idr, unsigned first_mb, unsigned st, unsigned fn,
                 bool field, bool bottom, unsigned pps,
                 unsigned fn_bits, std::uint64_t header_off) {
    auto rbsp = fixture::build_slice_rbsp(
        first_mb, st, pps, fn, fn_bits, field, bottom, idr);
    NalUnit nal;
    nal.nal_offset = header_off;
    nal.start_offset = header_off - 4;
    nal.nal_type = idr ? NAL_IDR : NAL_SLICE;
    nal.valid = true;
    std::vector<std::uint8_t> ebsp = escape_ebsp(rbsp);
    ebsp.insert(ebsp.begin(), fixture::nal_header(idr ? 3 : 2, nal.nal_type));
    nal.payload = std::move(ebsp);
    return nal;
}

struct Harness {
    SpsInfo frame_sps;
    SpsInfo field_sps;
    PpsInfo frame_pps;
    PpsInfo field_pps;
    ParameterSetStore store;
    Diagnostics diag;

    Harness() {
        frame_sps.sps_id = 0;
        frame_sps.frame_mbs_only_flag = true;
        frame_sps.log2_max_frame_num_minus4 = 0;  // 4-bit frame_num
        field_sps.sps_id = 1;
        field_sps.frame_mbs_only_flag = false;
        field_sps.log2_max_frame_num_minus4 = 2;  // 6-bit frame_num
        frame_pps.pps_id = 0;
        frame_pps.sps_id = 0;
        field_pps.pps_id = 1;
        field_pps.sps_id = 1;
        store.add_sps(frame_sps);
        store.add_sps(field_sps);
        store.add_pps(frame_pps);
        store.add_pps(field_pps);
    }
};

}  // namespace

TEST_CASE("multiple slices of one picture count as a single frame",
          "[access_unit]") {
    Harness h;
    AccessUnitAssembler au(h.store);
    au.handle_nal(make_vcl(true, 0, SLICE_I, 0, false, false, 0, 4, 10), h.diag);
    au.handle_nal(make_vcl(true, 120, SLICE_I, 0, false, false, 0, 4, 20), h.diag);
    au.finish(h.diag);
    REQUIRE(au.pictures().size() == 1);
    CHECK(au.pictures()[0].slice_count == 2);
    CHECK(au.stats().i == 1);
    CHECK(au.stats().frame_pics == 1);
}

TEST_CASE("frame_num change opens a new picture even at first_mb zero",
          "[access_unit]") {
    Harness h;
    AccessUnitAssembler au(h.store);
    au.handle_nal(make_vcl(true, 0, SLICE_I, 0, false, false, 0, 4, 10), h.diag);
    au.handle_nal(make_vcl(false, 0, SLICE_P, 1, false, false, 0, 4, 20), h.diag);
    au.handle_nal(make_vcl(false, 0, SLICE_B, 2, false, false, 0, 4, 30), h.diag);
    au.finish(h.diag);
    REQUIRE(au.pictures().size() == 3);
    CHECK(au.stats().i == 1);
    CHECK(au.stats().p == 1);
    CHECK(au.stats().b == 1);
}

TEST_CASE("complementary fields are two field pictures", "[access_unit]") {
    Harness h;
    AccessUnitAssembler au(h.store);
    au.handle_nal(make_vcl(false, 0, SLICE_B, 4, true, false, 1, 6, 10), h.diag);
    au.handle_nal(make_vcl(false, 0, SLICE_B, 4, true, true, 1, 6, 20), h.diag);
    au.finish(h.diag);
    REQUIRE(au.pictures().size() == 2);
    CHECK(au.stats().field_pics == 2);
    CHECK(au.stats().frame_pics == 0);
    CHECK(au.pictures()[0].bottom_field == false);
    CHECK(au.pictures()[1].bottom_field == true);
}

TEST_CASE("IDR pictures delimit GOPs with first-NAL offsets", "[access_unit]") {
    Harness h;
    AccessUnitAssembler au(h.store);
    au.handle_nal(make_vcl(false, 0, SLICE_P, 1, false, false, 0, 4, 10), h.diag);
    au.handle_nal(make_vcl(true, 0, SLICE_I, 0, false, false, 0, 4, 20), h.diag);
    au.handle_nal(make_vcl(false, 0, SLICE_P, 1, false, false, 0, 4, 30), h.diag);
    au.handle_nal(make_vcl(true, 0, SLICE_I, 0, false, false, 0, 4, 40), h.diag);
    au.finish(h.diag);

    CHECK(au.leading_pictures().size() == 1);
    REQUIRE(au.groups().size() == 2);
    CHECK(au.groups()[0].start_offset == 16);  // 20 - 4
    CHECK(au.groups()[0].closed);
    CHECK(au.groups()[1].start_offset == 36);  // 40 - 4
    CHECK_FALSE(au.groups()[1].closed);
}

TEST_CASE("slice referencing an undefined PPS keeps an error and continues",
          "[access_unit]") {
    Harness h;
    AccessUnitAssembler au(h.store);
    au.handle_nal(make_vcl(false, 0, SLICE_P, 0, false, false, 99, 4, 10),
                  h.diag);
    au.handle_nal(make_vcl(true, 0, SLICE_I, 0, false, false, 0, 4, 20),
                  h.diag);
    au.finish(h.diag);
    REQUIRE(h.diag.has_errors());
    CHECK(h.diag[0].offset == 10);
    // The bad picture is retained, then the valid IDR still forms a GOP.
    REQUIRE(au.pictures().size() == 2);
    CHECK(au.pictures()[0].parse_failed);
    CHECK(au.groups().size() == 1);
}

TEST_CASE("unsupported slice type lands in the unknown bucket",
          "[access_unit]") {
    Harness h;
    Diagnostics diag;
    // slice_type 10 is outside the 0..9 H.264 range.
    auto rbsp = fixture::build_slice_rbsp(0, 10, 0, 0, 4, false, false, false);
    NalUnit nal = make_vcl(false, 0, SLICE_P, 0, false, false, 0, 4, 10);
    std::vector<std::uint8_t> ebsp = escape_ebsp(rbsp);
    ebsp.insert(ebsp.begin(), fixture::nal_header(2, NAL_SLICE));
    nal.payload = ebsp;

    SliceHeader sh;
    CHECK_FALSE(parse_slice_header(nal, h.store, sh, diag));
    CHECK(sh.kind == FrameKind::Unknown);
    CHECK(diag[diag.size() - 1].offset == 10);
}
