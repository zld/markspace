// Tests for the rbsp module.
#include <cstdio>
#include <string>
#include <vector>

#include "annexb_fixture.hpp"
#include "bit_reader.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

void test_rbsp_removes_emulation_bytes() {
    const std::vector<std::uint8_t> ebsp = {0x00, 0x00, 0x03, 0x01,
                                            0xAA, 0x00, 0x00, 0x03, 0x03};
    RbspBuffer rbsp;
    Diagnostic diag;
    CHECK(build_rbsp(ebsp.data(), ebsp.size(), 100, rbsp, &diag));
    const std::vector<std::uint8_t> expected = {0x00, 0x00, 0x01, 0xAA,
                                                0x00, 0x00, 0x03};
    CHECK(rbsp.bytes == expected);
    CHECK_EQ(rbsp.removed_count, std::uint32_t{2});
    // RBSP index 3 maps to file offset 100 + 3 + 1 dropped byte.
    CHECK_EQ(rbsp.source_offset_of(3), std::uint64_t{104});
    CHECK_EQ(rbsp.source_offset_of(5), std::uint64_t{106});
}

void test_rbsp_zero_run_needs_two_zeros() {
    const std::vector<std::uint8_t> ebsp = {0x00, 0x03, 0x00, 0x00, 0x03};
    RbspBuffer rbsp;
    Diagnostic diag;
    CHECK(build_rbsp(ebsp.data(), ebsp.size(), 0, rbsp, &diag));
    const std::vector<std::uint8_t> expected = {0x00, 0x03, 0x00, 0x00};
    CHECK(rbsp.bytes == expected);
    CHECK_EQ(rbsp.removed_count, std::uint32_t{1});
}

void test_rbsp_flags_trailing_zeros() {
    const std::vector<std::uint8_t> ebsp = {0x80, 0x00, 0x00};
    RbspBuffer rbsp;
    Diagnostic diag;
    CHECK(build_rbsp(ebsp.data(), ebsp.size(), 7, rbsp, &diag));
    CHECK(diag.kind == ErrorKind::Truncated);
    CHECK_EQ(diag.offset, std::uint64_t{8});
}

void test_bit_reader_bounds() {
    RbspBuffer rbsp;
    rbsp.bytes = {0b1011'0010, 0b0100'0000};
    BitReader reader(rbsp);

    bool bit = false;
    CHECK(reader.read_bit(bit));
    CHECK(bit);
    std::uint32_t value = 0;
    CHECK(reader.read_bits(3, value));
    CHECK_EQ(value, std::uint32_t{3});
    CHECK(reader.read_bits(8, value));
    CHECK_EQ(value, std::uint32_t{0x24});
    CHECK_EQ(reader.bits_remaining(), std::size_t{4});
    CHECK(!reader.read_bits(5, value));
    CHECK(reader.read_bits(4, value));
    CHECK_EQ(value, std::uint32_t{0});
    CHECK(reader.exhausted());
    CHECK(!reader.read_bit(bit));
}

void test_bit_reader_trailing_bits_and_more_data() {
    selftest::BitWriter writer;
    writer.bits(0b101, 3);
    writer.rbsp_trailing_bits();

    RbspBuffer rbsp;
    rbsp.bytes = writer.bytes();
    BitReader reader(rbsp);
    CHECK(reader.more_rbsp_data());
    std::uint32_t value = 0;
    CHECK(reader.read_bits(3, value));
    CHECK_EQ(value, std::uint32_t{5});
    CHECK(!reader.more_rbsp_data());
    Diagnostic diag;
    CHECK(reader.read_rbsp_trailing_bits(&diag));
    CHECK(diag.kind == ErrorKind::None);

    // Payload holding only trailing bits has no data before the stop bit.
    selftest::BitWriter empty_writer;
    empty_writer.rbsp_trailing_bits();
    RbspBuffer empty_rbsp;
    empty_rbsp.bytes = empty_writer.bytes();
    BitReader empty_reader(empty_rbsp);
    CHECK(!empty_reader.more_rbsp_data());
    CHECK(empty_reader.read_rbsp_trailing_bits(&diag));
}

void test_trailing_bits_reject_missing_stop_bit() {
    RbspBuffer rbsp;
    rbsp.bytes = {0x00};
    BitReader reader(rbsp);
    Diagnostic diag;
    CHECK(!reader.read_rbsp_trailing_bits(&diag));
    CHECK(diag.kind == ErrorKind::Truncated);
}

}  // namespace

REGISTER_TEST(test_rbsp_removes_emulation_bytes);
REGISTER_TEST(test_rbsp_zero_run_needs_two_zeros);
REGISTER_TEST(test_rbsp_flags_trailing_zeros);
REGISTER_TEST(test_bit_reader_bounds);
REGISTER_TEST(test_bit_reader_trailing_bits_and_more_data);
REGISTER_TEST(test_trailing_bits_reject_missing_stop_bit);
