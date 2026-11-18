// Tests for the slice module.
#include <cstdio>
#include <string>
#include <vector>

#include "pps_parser.hpp"
#include "pps_slice_fixture.hpp"
#include "slice_parser.hpp"
#include "sps_fixture.hpp"
#include "sps_parser.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

struct ParamSetPair {
    selftest::SpsFixture sps_fixture;
    selftest::PpsFixture pps_fixture;
    Sps sps;
    Pps pps;
};

ParamSetPair make_param_sets(const selftest::SpsFixture& sps_fixture,
                             const selftest::PpsFixture& pps_fixture) {
    ParamSetPair pair;
    pair.sps_fixture = sps_fixture;
    pair.pps_fixture = pps_fixture;
    Diagnostic diag;
    ExpGolombStats stats;
    const std::vector<std::uint8_t> sps_bytes =
        selftest::escape_rbsp(selftest::build_sps_rbsp(sps_fixture));
    const std::vector<std::uint8_t> pps_bytes =
        selftest::escape_rbsp(selftest::build_pps_rbsp(pps_fixture));
    CHECK(parse_sps(sps_bytes.data(), sps_bytes.size(), 5, 0, stats, pair.sps, &diag));
    CHECK(parse_pps(pps_bytes.data(), pps_bytes.size(), 20, 15, stats, pair.pps, &diag));
    return pair;
}

SliceHeader parse_slice_from(const selftest::SliceFixture& slice_fixture,
                             const ParamSetPair& pair, Diagnostic& diag,
                             int nal_unit_type = 5) {
    const std::vector<std::uint8_t> payload = selftest::escape_rbsp(
        selftest::build_slice_header_rbsp(slice_fixture, pair.sps_fixture,
                                          pair.pps_fixture));
    SliceHeader header;
    const int nal_ref_idc = slice_fixture.nal_ref_idc;
    ExpGolombStats stats;
    SliceContext context;
    context.sps = &pair.sps;
    context.pps = &pair.pps;
    parse_slice_header(payload.data(), payload.size(), 30, nal_ref_idc,
                       nal_unit_type, context, stats, header, &diag);
    return header;
}

void test_pps_parses_core_fields() {
    selftest::SpsFixture sps_fixture;
    selftest::PpsFixture pps_fixture;
    pps_fixture.entropy_coding_mode = true;
    pps_fixture.bottom_field_pic_order_in_frame_present = true;
    pps_fixture.num_ref_idx_l0_default_active_minus1 = 2;
    pps_fixture.weighted_bipred_idc = 2;
    pps_fixture.pic_init_qp_minus26 = -3;
    pps_fixture.chroma_qp_index_offset = 5;
    const ParamSetPair pair = make_param_sets(sps_fixture, pps_fixture);
    CHECK_EQ(pair.pps.id, std::uint32_t{0});
    CHECK_EQ(pair.pps.sps_id, std::uint32_t{0});
    CHECK(pair.pps.entropy_coding_mode);
    CHECK(pair.pps.bottom_field_pic_order_in_frame_present);
    CHECK_EQ(pair.pps.num_ref_idx_l0_default_active, std::uint32_t{3});
    CHECK_EQ(pair.pps.num_ref_idx_l1_default_active, std::uint32_t{1});
    CHECK_EQ(pair.pps.weighted_bipred_idc, std::uint32_t{2});
    CHECK_EQ(pair.pps.pic_init_qp_minus26, std::int64_t{-3});
    CHECK_EQ(pair.pps.chroma_qp_index_offset, std::int64_t{5});
}

void test_slice_header_idr_fields() {
    const ParamSetPair pair = make_param_sets(selftest::SpsFixture(),
                                              selftest::PpsFixture());
    selftest::SliceFixture slice;
    slice.first_mb_in_slice = 0;
    slice.slice_type = 7;  // all-I variant
    slice.frame_num = 5;
    slice.idr_pic_id = 1;
    slice.pic_order_cnt_lsb = 17;
    Diagnostic diag;
    const SliceHeader header = parse_slice_from(slice, pair, diag);
    CHECK(diag.kind == ErrorKind::None);
    CHECK_EQ(header.first_mb_in_slice, std::uint32_t{0});
    CHECK(header.slice_type == SliceType::I);
    CHECK_EQ(header.frame_num, std::uint32_t{5});
    CHECK_EQ(header.idr_pic_id, std::uint32_t{1});
    CHECK(header.have_poc_lsb);
    CHECK_EQ(header.pic_order_cnt_lsb, std::uint32_t{17});
    CHECK(header.is_idr);
    CHECK(!header.field_pic_flag);
    CHECK(header.is_slice_group_start());
    CHECK_EQ(header.picture_key(), std::uint32_t{5u << 2});
}

void test_slice_header_field_picture() {
    selftest::SpsFixture sps_fixture;
    sps_fixture.frame_mbs_only = false;
    sps_fixture.pic_height_in_map_units_minus1 = 22;  // 2 map units -> 736 px
    const ParamSetPair pair = make_param_sets(sps_fixture, selftest::PpsFixture());
    selftest::SliceFixture slice;
    slice.frame_num = 2;
    slice.field_pic_flag = true;
    slice.bottom_field_flag = true;
    Diagnostic diag;
    const SliceHeader header = parse_slice_from(slice, pair, diag);
    CHECK(diag.kind == ErrorKind::None);
    CHECK(header.field_pic_flag);
    CHECK(header.bottom_field_flag);
    CHECK_EQ(header.picture_key(), std::uint32_t{(2u << 2) | 2u});
}

void test_slice_header_non_idr_p_slice() {
    const ParamSetPair pair = make_param_sets(selftest::SpsFixture(),
                                              selftest::PpsFixture());
    selftest::SliceFixture slice;
    slice.slice_type = 0;  // P
    slice.frame_num = 9;
    slice.is_idr = false;
    slice.nal_ref_idc = 2;
    slice.pic_order_cnt_lsb = 4;
    slice.first_mb_in_slice = 80;  // second slice of the same picture
    Diagnostic diag;
    const SliceHeader header =
        parse_slice_from(slice, pair, diag, /*nal_unit_type=*/1);
    CHECK(diag.kind == ErrorKind::None);
    CHECK(header.slice_type == SliceType::P);
    CHECK(!header.is_idr);
    CHECK_EQ(header.frame_num, std::uint32_t{9});
    CHECK_EQ(header.first_mb_in_slice, std::uint32_t{80});
    CHECK(!header.is_slice_group_start());
}

void test_slice_header_missing_parameter_set() {
    SliceContext context;
    Diagnostic diag;
    ExpGolombStats stats;
    const std::vector<std::uint8_t> payload = {0x88, 0x84, 0x00};
    SliceHeader header;
    CHECK(!parse_slice_header(payload.data(), payload.size(), 0, 3, 5, context,
                              stats, header, &diag));
    CHECK(diag.kind == ErrorKind::UnknownPps);
}

void test_slice_header_rejects_wild_slice_type() {
    const ParamSetPair pair = make_param_sets(selftest::SpsFixture(),
                                              selftest::PpsFixture());
    selftest::SliceFixture slice;
    slice.slice_type = 12;  // beyond the 9 legal values
    Diagnostic diag;
    parse_slice_from(slice, pair, diag);
    CHECK(diag.kind == ErrorKind::Unsupported);
}

}  // namespace

REGISTER_TEST(test_pps_parses_core_fields);
REGISTER_TEST(test_slice_header_idr_fields);
REGISTER_TEST(test_slice_header_field_picture);
REGISTER_TEST(test_slice_header_non_idr_p_slice);
REGISTER_TEST(test_slice_header_missing_parameter_set);
REGISTER_TEST(test_slice_header_rejects_wild_slice_type);
