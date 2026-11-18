// Tests for the nal module.
#include <cstdio>
#include <string>
#include <vector>

#include "annexb_fixture.hpp"
#include "nal_parser.hpp"
#include "byte_source.hpp"
#include "test_util.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

NalScanResult scan_bytes(const std::vector<std::uint8_t>& bytes) {
    const std::string path = selftest::write_temp("h264inspect_scan.bin",
                                        std::string(bytes.begin(), bytes.end()));
    ByteSource source;
    Diagnostic err;
    CHECK(source.open(path, ByteSource::kDefaultMaxSize, &err));
    NalScanResult result = scan_nal_units(source);
    std::remove(path.c_str());
    return result;
}

void test_nal_scan_three_and_four_byte_codes() {
    const auto bytes = selftest::concat({
        selftest::annexb_nal(0x67, {0xAA, 0xBB}, true),   // SPS, 4-byte code
        selftest::annexb_nal(0x68, {0x11}, false),       // PPS, 3-byte code
        selftest::annexb_nal(0x65, {0x01, 0x02, 0x03}, false),  // IDR
    });
    const NalScanResult result = scan_bytes(bytes);
    CHECK_EQ(result.units.size(), std::size_t{3});
    CHECK(result.diagnostics.empty());

    CHECK_EQ(result.units[0].start_code_offset, std::uint64_t{0});
    CHECK_EQ(result.units[0].start_code_length, std::uint32_t{4});
    CHECK_EQ(result.units[0].payload_offset, std::uint64_t{4});
    CHECK_EQ(result.units[0].payload_length, std::uint64_t{2});
    CHECK_EQ(result.units[0].unit_type(), 7);
    CHECK_EQ(result.units[0].nal_ref_idc(), 3);

    CHECK_EQ(result.units[1].start_code_offset, std::uint64_t{7});
    CHECK_EQ(result.units[1].start_code_length, std::uint32_t{3});
    CHECK_EQ(result.units[1].payload_offset, std::uint64_t{10});
    CHECK_EQ(result.units[1].payload_length, std::uint64_t{1});
    CHECK_EQ(result.units[1].unit_type(), 8);

    CHECK_EQ(result.units[2].start_code_offset, std::uint64_t{12});
    CHECK_EQ(result.units[2].unit_type(), 5);
    CHECK_EQ(result.units[2].payload_length, std::uint64_t{3});
}

void test_nal_scan_leading_garbage_and_zero_padding() {
    std::vector<std::uint8_t> bytes = {0xDE, 0xAD, 0x00, 0x00};
    selftest::append(bytes, selftest::annexb_nal(0x67, {0x42}, true));
    // Three padding zeros, none of which can extend into a start code: the
    // next code is a full 4-byte code at offset 13.
    bytes.insert(bytes.end(), {0x00, 0x00, 0x00});
    selftest::append(bytes, selftest::annexb_nal(0x68, {0xCE}, true));
    bytes.insert(bytes.end(), {0x00, 0x00, 0x00});

    const NalScanResult result = scan_bytes(bytes);
    CHECK_EQ(result.units.size(), std::size_t{2});
    CHECK_EQ(result.summary.leading_garbage_bytes, std::uint64_t{2});
    CHECK_EQ(result.summary.leading_zero_bytes, std::uint64_t{2});
    CHECK_EQ(result.summary.inter_nal_zero_bytes, std::uint32_t{3});
    CHECK_EQ(result.summary.trailing_zero_bytes, std::uint32_t{3});
    CHECK_EQ(result.units[0].start_code_offset, std::uint64_t{4});
    CHECK_EQ(result.units[0].payload_length, std::uint64_t{1});
    CHECK_EQ(result.units[1].start_code_offset, std::uint64_t{13});
    CHECK_EQ(result.units[1].payload_offset, std::uint64_t{17});
    CHECK(!result.diagnostics.empty());
}

void test_nal_scan_consecutive_and_empty_codes() {
    const auto bytes = selftest::concat({
        selftest::annexb_nal(0x67, {0x11}, true),
        {0x00, 0x00, 0x01},                  // start code, then immediately another
        {0x00, 0x00, 0x00, 0x01},
        selftest::annexb_nal(0x68, {}, true),  // empty payload (header only)
        selftest::annexb_nal(0x09, {0xF0}, true),
    });
    const NalScanResult result = scan_bytes(bytes);
    // Both intermediate codes are directly followed by another start code, so
    // neither can carry a NAL header byte.
    CHECK_EQ(result.summary.consecutive_start_codes, std::uint32_t{2});
    CHECK_EQ(result.summary.empty_nal_units, std::uint32_t{1});
    CHECK_EQ(result.units.size(), std::size_t{3});
    CHECK_EQ(result.units[1].unit_type(), 8);
    CHECK_EQ(result.units[1].payload_length, std::uint64_t{0});
    CHECK_EQ(result.units[2].unit_type(), 9);
}

void test_nal_scan_truncated_tail_and_header_flags() {
    std::vector<std::uint8_t> bytes = selftest::annexb_nal(0xE7, {0x01});  // forbidden bit
    bytes.insert(bytes.end(), {0x00, 0x00, 0x00, 0x01});  // start code with no header
    const NalScanResult result = scan_bytes(bytes);
    CHECK_EQ(result.units.size(), std::size_t{1});
    CHECK(result.units[0].forbidden_zero_bit());
    bool saw_forbidden = false;
    bool saw_truncated = false;
    for (const auto& d : result.diagnostics) {
        saw_forbidden |= d.kind == ErrorKind::IllegalValue;
        saw_truncated |= d.kind == ErrorKind::Truncated && d.offset == std::uint64_t{6};
    }
    CHECK(saw_forbidden);
    CHECK(saw_truncated);
}

void test_nal_scan_empty_input() {
    const NalScanResult result = scan_bytes({});
    CHECK_EQ(result.units.size(), std::size_t{0});
    CHECK(result.diagnostics.empty());
    CHECK_EQ(result.summary.total_bytes, std::uint64_t{0});
}

}  // namespace

REGISTER_TEST(test_nal_scan_three_and_four_byte_codes);
REGISTER_TEST(test_nal_scan_leading_garbage_and_zero_padding);
REGISTER_TEST(test_nal_scan_consecutive_and_empty_codes);
REGISTER_TEST(test_nal_scan_truncated_tail_and_header_flags);
REGISTER_TEST(test_nal_scan_empty_input);
