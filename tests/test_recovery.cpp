// Tests for recovering from stream errors.
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

void test_unknown_pps_keeps_analyzing() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::uint8_t> bytes = selftest::build_sps_nal(sps);
    selftest::append(bytes, selftest::build_pps_nal(pps));
    selftest::SliceFixture slice;
    slice.pps_id = 5;  // never defined
    slice.slice_type = 2;
    const std::vector<std::uint8_t> rbsp = selftest::escape_rbsp(
        selftest::build_slice_header_rbsp(slice, sps, pps));
    selftest::append(bytes, selftest::annexb_nal(0x65, rbsp));
    // A valid picture after the broken one must still be reported.
    selftest::append(bytes, selftest::build_stream(sps, pps,
                                                   {{{0, 2, 0, 0, 1, true, 3,
                                                     false, false, 0}}}));
    const AnalysisResult result = analyze_bytes(bytes);
    bool saw_unknown_pps = false;
    for (const Diagnostic& d : result.errors) {
        saw_unknown_pps |= d.kind == ErrorKind::UnknownPps;
    }
    CHECK(saw_unknown_pps);
    // The broken picture and the following valid one are both reported; the
    // broken one carries an explicit uncertainty note.
    CHECK_EQ(result.frames.size(), std::size_t{2});
    CHECK(result.frames[0].had_errors);
    CHECK(!result.frames[0].uncertainty.empty());
    CHECK(!result.frames[1].had_errors);
}

void test_truncated_slice_header_is_reported() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::uint8_t> bytes = selftest::build_sps_nal(sps);
    selftest::append(bytes, selftest::build_pps_nal(pps));

    selftest::SliceFixture slice;
    slice.slice_type = 0;  // P slice needs more header fields than I
    slice.is_idr = false;
    slice.frame_num = 1;
    slice.nal_ref_idc = 2;
    slice.include_slice_data = false;
    const std::vector<std::uint8_t> rbsp = selftest::escape_rbsp(
        selftest::build_slice_header_rbsp(slice, sps, pps));
    // Keep only the leading bytes so the header stops mid-way.
    std::vector<std::uint8_t> cut(selftest::annexb_nal(0x41, rbsp));
    cut.resize(cut.size() - 1);
    selftest::append(bytes, cut);

    const AnalysisResult result = analyze_bytes(bytes);
    bool saw_truncated = false;
    for (const Diagnostic& d : result.errors) {
        saw_truncated |= d.kind == ErrorKind::Truncated;
    }
    CHECK(saw_truncated);
    // The identity fields were readable, so a frame is still reported and the
    // uncertainty is explicit rather than silent.
    CHECK_EQ(result.frames.size(), std::size_t{1});
    CHECK(result.frames[0].had_errors);
}

void test_unsupported_extension_nal_is_reported() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::uint8_t> bytes = selftest::build_stream(
        sps, pps, {{{0, 2, 0, 0, 1, true, 3, false, false, 0}}});
    // Slice extension NAL (type 20) cannot be parsed by the base slice reader.
    selftest::append(bytes, selftest::annexb_nal(0x74, {0x01, 0x02, 0x03}));
    const AnalysisResult result = analyze_bytes(bytes);
    bool saw_unsupported = false;
    for (const Diagnostic& d : result.errors) {
        saw_unsupported |= d.kind == ErrorKind::Unsupported &&
                           d.message.find("slice_extension") != std::string::npos;
    }
    CHECK(saw_unsupported);
    CHECK_EQ(result.frames.size(), std::size_t{1});
}

void test_illegal_sps_dimensions_reported_once() {
    selftest::SpsFixture sps;
    sps.pic_width_in_mbs_minus1 = 4095;  // 65536 px, above the 16384 limit
    selftest::PpsFixture pps;
    const std::vector<std::uint8_t> bytes = selftest::build_stream(
        sps, pps, {{{0, 2, 0, 0, 1, true, 3, false, false, 0}}});
    const AnalysisResult result = analyze_bytes(bytes);
    bool saw_limit = false;
    for (const Diagnostic& d : result.errors) {
        saw_limit |= d.kind == ErrorKind::LimitExceeded &&
                     d.message.find("exceeds limit") != std::string::npos;
    }
    CHECK(saw_limit);
    // The slice cannot be analyzed, but it is still reported as an uncertain
    // picture rather than silently dropped.
    CHECK_EQ(result.frames.size(), std::size_t{1});
    CHECK(result.frames[0].had_errors);
    for (const Diagnostic& d : result.errors) {
        CHECK(d.offset < bytes.size());
    }
}

void test_repeated_identical_sps_is_not_a_group_boundary() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    std::vector<std::vector<selftest::SliceSpec>> aus;
    aus.push_back({{0, 2, 0, 0, 1, true, 3, false, false, 0}});
    aus.push_back({{0, 0, 1, 4, 0, false, 2, false, false, 0}});
    aus.push_back({{0, 0, 2, 8, 0, false, 2, false, false, 0}});

    std::vector<std::uint8_t> bytes = selftest::build_sps_nal(sps);
    selftest::append(bytes, selftest::build_pps_nal(pps));
    // Identical SPS repeated mid-stream: not a parameter set change.
    for (const auto& au : aus) {
        for (const auto& s : au) {
            selftest::SliceFixture slice;
            slice.first_mb_in_slice = s.first_mb;
            slice.slice_type = s.slice_type;
            slice.frame_num = s.frame_num;
            slice.pic_order_cnt_lsb = s.pic_order_cnt_lsb;
            slice.idr_pic_id = s.idr_pic_id;
            slice.is_idr = s.is_idr;
            slice.nal_ref_idc = s.nal_ref_idc;
            selftest::append(bytes,
                             selftest::annexb_nal(
                                 (s.nal_ref_idc << 5) | (s.is_idr ? 5 : 1),
                                 selftest::escape_rbsp(
                                     selftest::build_slice_header_rbsp(slice, sps,
                                                                       pps))));
        }
        selftest::append(bytes, selftest::build_sps_nal(sps));
    }
    const AnalysisResult result = analyze_bytes(bytes);
    CHECK_EQ(result.groups.size(), std::size_t{1});
    CHECK_EQ(result.frames.size(), std::size_t{3});
    CHECK_GT(result.sps_list.size(), std::size_t{1});
}

void test_alternate_pps_reference_is_not_a_boundary() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    selftest::PpsFixture pps2;
    pps2.id = 1;
    pps2.pic_init_qp_minus26 = 2;  // distinct content, but never activated
    std::vector<std::uint8_t> bytes = selftest::build_sps_nal(sps);
    selftest::append(bytes, selftest::build_pps_nal(pps));
    selftest::append(bytes, selftest::build_pps_nal(pps2));
    selftest::append(bytes, selftest::build_stream(
                                 sps, pps,
                                 {{{0, 2, 0, 0, 1, true, 3, false, false, 0}},
                                  {{0, 0, 1, 4, 0, false, 2, false, false, 0}}}));
    const AnalysisResult result = analyze_bytes(bytes);
    CHECK_EQ(result.groups.size(), std::size_t{1});
    CHECK_EQ(result.groups[0].frame_count, std::uint32_t{2});
    CHECK_EQ(result.pps_list.size(), std::size_t{3});
}

void test_huge_nal_is_skipped_not_fatal() {
    // A NAL above the 16 MiB payload limit must be reported and skipped, and
    // the rest of the stream must still be analyzed.
    std::vector<std::uint8_t> bytes = selftest::build_reference_sample();
    selftest::append(bytes, selftest::annexb_nal(
                                 0x61, std::vector<std::uint8_t>(
                                           16u * 1024u * 1024u + 16u, 0x55)));
    selftest::append(bytes, selftest::build_sps_nal(selftest::SpsFixture()));
    const AnalysisResult result = analyze_bytes(bytes);
    bool saw_limit = false;
    for (const Diagnostic& d : result.errors) {
        saw_limit |= d.kind == ErrorKind::LimitExceeded;
    }
    CHECK(saw_limit);
    CHECK_EQ(result.frames.size(), std::size_t{3});
}

void test_group_start_includes_preceding_parameter_set() {
    selftest::SpsFixture sps;
    selftest::PpsFixture pps;
    // IDR picture preceded by its SPS: the group must start at the SPS NAL.
    std::vector<std::uint8_t> bytes;
    selftest::append(bytes, selftest::annexb_nal(0x09, {0xF0}));  // AUD
    const std::size_t sps_offset = bytes.size();
    selftest::append(bytes, selftest::build_sps_nal(sps));
    selftest::append(bytes, selftest::build_pps_nal(pps));
    selftest::append(bytes, selftest::build_stream(
                                 sps, pps,
                                 {{{0, 2, 0, 0, 1, true, 3, false, false, 0}}}));
    const AnalysisResult result = analyze_bytes(bytes);
    CHECK_EQ(result.groups.size(), std::size_t{1});
    CHECK_EQ(result.groups[0].start_nal_offset, std::uint64_t{0});
    CHECK_EQ(result.groups[0].slice_count, std::uint32_t{1});
    CHECK_GT(sps_offset, std::size_t{0});
}

}  // namespace

REGISTER_TEST(test_unknown_pps_keeps_analyzing);
REGISTER_TEST(test_truncated_slice_header_is_reported);
REGISTER_TEST(test_unsupported_extension_nal_is_reported);
REGISTER_TEST(test_illegal_sps_dimensions_reported_once);
REGISTER_TEST(test_repeated_identical_sps_is_not_a_group_boundary);
REGISTER_TEST(test_alternate_pps_reference_is_not_a_boundary);
REGISTER_TEST(test_huge_nal_is_skipped_not_fatal);
REGISTER_TEST(test_group_start_includes_preceding_parameter_set);
