// Tests for access-unit segmentation and grouping.
#include <cstdio>
#include <string>
#include <vector>

#include "access_unit.hpp"
#include "byte_source.hpp"
#include "stream_builder.hpp"
#include "test_util.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

AnalysisResult analyze_bytes(const std::vector<std::uint8_t>& bytes) {
    const std::string path = selftest::write_temp("h264inspect_stream.bin",
                                        std::string(bytes.begin(), bytes.end()));
    ByteSource source;
    Diagnostic err;
    CHECK(source.open(path, ByteSource::kDefaultMaxSize, &err));
    AnalysisResult result = analyze_stream(source);
    std::remove(path.c_str());
    return result;
}

void test_reference_sample_structure() {
    const AnalysisResult result = analyze_bytes(selftest::build_reference_sample());
    CHECK(result.errors.empty());
    CHECK_EQ(result.sps_list.size(), std::size_t{1});
    CHECK_EQ(result.pps_list.size(), std::size_t{1});
    CHECK_EQ(result.sps_list[0].width, std::uint32_t{320});
    CHECK_EQ(result.sps_list[0].height, std::uint32_t{192});

    CHECK_EQ(result.frames.size(), std::size_t{3});
    CHECK(result.frames[0].type == SliceType::I);
    CHECK(result.frames[1].type == SliceType::P);
    CHECK(result.frames[2].type == SliceType::B);
    CHECK_EQ(result.frames[0].slice_count, std::uint32_t{1});

    CHECK_EQ(result.groups.size(), std::size_t{1});
    CHECK_EQ(result.groups[0].frame_count, std::uint32_t{3});
    CHECK_EQ(result.groups[0].slice_count, std::uint32_t{3});
    CHECK(result.groups[0].first_frame_type == SliceType::I);
    CHECK(result.groups[0].reason == BoundaryReason::FirstPicture);
    CHECK(result.groups[0].complete);
}

void test_multi_slice_picture_is_one_frame() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    aus.push_back({
        {0, 2, 0, 0, 1, true, 3, false, false, 0},
        {40, 2, 0, 0, 1, true, 3, false, false, 0},
        {90, 2, 0, 0, 1, true, 3, false, false, 0},
        {150, 2, 0, 0, 1, true, 3, false, false, 0},
    });
    const AnalysisResult result =
        analyze_bytes(selftest::build_stream(sps, pps, aus));
    CHECK_EQ(result.pictures.size(), std::size_t{1});
    CHECK_EQ(result.frames.size(), std::size_t{1});
    CHECK_EQ(result.frames[0].slice_count, std::uint32_t{4});
    CHECK(!result.frames[0].had_errors);
    CHECK_EQ(result.groups.size(), std::size_t{1});
    CHECK_EQ(result.groups[0].slice_count, std::uint32_t{4});
}

void test_out_of_order_macroblocks_stay_in_picture() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    // Arbitrary slice order (ASO): descending first_mb must not split the AU.
    aus.push_back({
        {0, 2, 0, 0, 1, true, 3, false, false, 0},
        {120, 2, 0, 0, 1, true, 3, false, false, 0},
        {40, 2, 0, 0, 1, true, 3, false, false, 0},
    });
    const AnalysisResult result =
        analyze_bytes(selftest::build_stream(sps, pps, aus));
    CHECK_EQ(result.frames.size(), std::size_t{1});
    CHECK_EQ(result.frames[0].slice_count, std::uint32_t{3});
    CHECK(result.pictures[0].slices_out_of_order);
    CHECK(result.groups[0].uncertain);
}

void test_idr_starts_new_group() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    aus.push_back({{0, 2, 0, 0, 1, true, 3, false, false, 0}});
    aus.push_back({{0, 0, 1, 4, 0, false, 2, false, false, 0}});
    aus.push_back({{0, 2, 0, 0, 2, true, 3, false, false, 0}});
    const AnalysisResult result =
        analyze_bytes(selftest::build_stream(sps, pps, aus));
    CHECK_EQ(result.groups.size(), std::size_t{2});
    CHECK(result.groups[0].reason == BoundaryReason::FirstPicture);
    CHECK(result.groups[1].reason == BoundaryReason::IdrPicture);
    CHECK_EQ(result.groups[0].frame_count, std::uint32_t{2});
    CHECK_EQ(result.groups[1].frame_count, std::uint32_t{1});
    CHECK(result.groups[1].first_frame_type == SliceType::I);
}

void test_interlaced_fields_pair_into_one_frame() {
    selftest::SpsFixture sps;
    sps.frame_mbs_only = false;
    sps.pic_height_in_map_units_minus1 = 22;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    // Top and bottom fields of the same frame share frame_num and POC.
    aus.push_back({{0, 2, 0, 0, 1, true, 3, true, false, 0}});
    aus.push_back({{0, 2, 0, 0, 1, true, 3, true, true, 0}});
    aus.push_back({{0, 2, 1, 2, 0, false, 3, true, false, 0}});
    aus.push_back({{0, 2, 1, 2, 0, false, 3, true, true, 0}});
    const AnalysisResult result =
        analyze_bytes(selftest::build_stream(sps, pps, aus));
    CHECK_EQ(result.pictures.size(), std::size_t{4});
    CHECK_EQ(result.frames.size(), std::size_t{2});
    CHECK(result.frames[0].field_pair);
    CHECK(result.frames[1].field_pair);
    CHECK(!result.frames[0].isolated_field);
    CHECK_EQ(result.frames[0].slice_count, std::uint32_t{2});
    CHECK_EQ(result.groups.size(), std::size_t{1});
    CHECK_EQ(result.groups[0].field_count, std::uint32_t{4});
    CHECK_EQ(result.groups[0].frame_count, std::uint32_t{2});
}

void test_lone_field_is_flagged() {
    selftest::SpsFixture sps;
    sps.frame_mbs_only = false;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    aus.push_back({{0, 2, 0, 0, 1, true, 3, true, false, 0}});
    aus.push_back({{0, 2, 1, 2, 1, true, 3, true, true, 0}});  // frame 1: bottom only
    const AnalysisResult result =
        analyze_bytes(selftest::build_stream(sps, pps, aus));
    CHECK_EQ(result.frames.size(), std::size_t{2});
    CHECK(result.frames[0].isolated_field);
    CHECK(result.frames[1].isolated_field);
    CHECK(result.frames[1].bottom_field);
    CHECK(!result.frames[0].field_pair);
}

void test_parameter_set_change_starts_group() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    aus.push_back({{0, 2, 0, 0, 1, true, 3, false, false, 0}});
    aus.push_back({{0, 0, 1, 4, 0, false, 2, false, false, 0}});

    std::vector<std::uint8_t> bytes = selftest::build_stream(sps, pps, aus);
    // A different PPS definition (different QP) before the next non-IDR picture.
    selftest::PpsFixture changed = pps;
    changed.pic_init_qp_minus26 = 6;
    selftest::append(bytes, selftest::build_pps_nal(changed));
    selftest::append(bytes,
                     selftest::build_stream(sps, changed,
                                            {{{0, 0, 2, 8, 0, false, 2, false,
                                                false, 0}}}));
    const AnalysisResult result = analyze_bytes(bytes);
    CHECK_EQ(result.groups.size(), std::size_t{2});
    CHECK(result.groups[1].reason == BoundaryReason::ParameterSetChange);
    CHECK_EQ(result.groups[0].frame_count, std::uint32_t{2});
}

void test_empty_stream() {
    const AnalysisResult result = analyze_bytes({});
    CHECK(result.errors.empty());
    CHECK(result.frames.empty());
    CHECK(result.groups.empty());
}

void test_uncertain_boundary_is_surfaced() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::uint8_t> bytes = selftest::build_stream(
        sps, pps, {{{0, 2, 0, 0, 1, true, 3, false, false, 0}},
                   {{0, 0, 1, 4, 0, false, 2, false, false, 0}}});
    const AnalysisResult result = analyze_bytes(bytes);
    CHECK(!result.has_uncertain_boundaries());
}

}  // namespace

REGISTER_TEST(test_reference_sample_structure);
REGISTER_TEST(test_multi_slice_picture_is_one_frame);
REGISTER_TEST(test_out_of_order_macroblocks_stay_in_picture);
REGISTER_TEST(test_idr_starts_new_group);
REGISTER_TEST(test_interlaced_fields_pair_into_one_frame);
REGISTER_TEST(test_lone_field_is_flagged);
REGISTER_TEST(test_parameter_set_change_starts_group);
REGISTER_TEST(test_empty_stream);
REGISTER_TEST(test_uncertain_boundary_is_surfaced);
