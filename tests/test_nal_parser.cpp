#include <catch2/catch_test_macros.hpp>

#include "diagnostics.hpp"
#include "nal_parser.hpp"

#include <vector>

using namespace h264;

namespace {

struct ScanResult {
    Diagnostics diag;
    std::vector<NalUnit> nals;
};

ScanResult scan_all(const std::vector<std::uint8_t>& bytes,
                    std::uint64_t cap = NalScanner::kDefaultMaxNalBytes) {
    ScanResult r;
    NalScanner scanner(r.diag, cap);
    // Feed one byte at a time to prove chunk boundaries do not matter.
    for (std::uint8_t b : bytes) {
        scanner.feed(&b, 1, r.nals);
    }
    scanner.finish(r.nals);
    return r;
}

}  // namespace

TEST_CASE("three and four byte start codes both delimit NAL units",
          "[nal]") {
    const std::vector<std::uint8_t> bytes = {
        0, 0, 0, 1, 0x67, 0xAA,       // 4-byte start, SPS
        0, 0, 1, 0x65, 0xBB, 0xCC};   // 3-byte start, IDR
    auto r = scan_all(bytes);
    REQUIRE_FALSE(r.diag.has_errors());
    REQUIRE(r.nals.size() == 2);
    CHECK(r.nals[0].start_offset == 0);
    CHECK(r.nals[0].nal_offset == 4);
    CHECK(r.nals[0].nal_type == NAL_SPS);
    CHECK(r.nals[0].nal_ref_idc == 3);
    CHECK(r.nals[1].start_offset == 6);
    CHECK(r.nals[1].nal_offset == 9);
    CHECK(r.nals[1].nal_type == NAL_IDR);
}

TEST_CASE("emulation prevention bytes are removed from payloads", "[nal]") {
    const std::vector<std::uint8_t> bytes = {
        0, 0, 1, 0x67, 0x00, 0x00, 0x03, 0x01, 0x00, 0x00, 0x03, 0x00, 0x02};
    auto r = scan_all(bytes);
    REQUIRE_FALSE(r.diag.has_errors());
    const std::vector<std::uint8_t> expected = {
        0x67, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02};
    CHECK(r.nals[0].payload == expected);
}

TEST_CASE("empty NAL between start codes records an offset error", "[nal]") {
    const std::vector<std::uint8_t> bytes = {0, 0, 1, 0, 0, 1, 0x67, 0x10};
    auto r = scan_all(bytes);
    REQUIRE(r.diag.has_errors());
    CHECK(r.diag[0].offset == 0);
    REQUIRE(r.nals.size() == 1);
    CHECK(r.nals[0].nal_type == NAL_SPS);
}

TEST_CASE("input with no start code is an error", "[nal]") {
    const std::vector<std::uint8_t> bytes = {0x41, 0x42, 0x43};
    auto r = scan_all(bytes);
    REQUIRE(r.diag.has_errors());
    CHECK(r.nals.empty());
    CHECK(r.diag[0].offset == 0);
}

TEST_CASE("forbidden_zero_bit invalidates the NAL but scanning continues",
          "[nal]") {
    const std::vector<std::uint8_t> bytes = {
        0, 0, 1, 0xE7, 0x10,        // forbidden bit set
        0, 0, 1, 0x65, 0x20};       // valid IDR after it
    auto r = scan_all(bytes);
    REQUIRE(r.diag.has_errors());
    REQUIRE(r.nals.size() == 2);
    CHECK_FALSE(r.nals[0].valid);
    CHECK(r.nals[1].valid);
    CHECK(r.nals[1].nal_type == NAL_IDR);
}

TEST_CASE("NAL larger than the byte cap reports and truncates", "[nal]") {
    std::vector<std::uint8_t> bytes = {0, 0, 1, 0x09};
    for (int i = 0; i < 50; ++i) bytes.push_back(0xAB);
    auto r = scan_all(bytes, /*cap=*/10);
    REQUIRE(r.diag.has_errors());
    CHECK(r.nals[0].size_exceeded);
    CHECK_FALSE(r.nals[0].valid);
    // Header plus at most cap-1 retained payload bytes.
    CHECK(r.nals[0].payload.size() <= 10);
}

TEST_CASE("trailing NAL without a following start code is flushed at EOF",
          "[nal]") {
    const std::vector<std::uint8_t> bytes = {0, 0, 1, 0x67, 0x11, 0x22};
    auto r = scan_all(bytes);
    REQUIRE_FALSE(r.diag.has_errors());
    REQUIRE(r.nals.size() == 1);
    CHECK(r.nals[0].raw_size == 3);
}
