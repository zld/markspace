// Tests for the exp golomb module.
#include <cstdio>
#include <string>
#include <vector>

#include "annexb_fixture.hpp"
#include "exp_golomb.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

void test_exp_golomb_round_trip() {
    selftest::BitWriter writer;
    const std::vector<std::uint32_t> ue_values = {0, 1, 2, 3, 4, 7, 8, 63, 64, 1000};
    const std::vector<std::int32_t> se_values = {0, 1, -1, 2, -2, 7, -7, 128, -129};
    for (std::uint32_t v : ue_values) writer.ue(v);
    for (std::int32_t v : se_values) writer.se(v);
    writer.rbsp_trailing_bits();

    RbspBuffer rbsp;
    rbsp.bytes = writer.bytes();
    BitReader reader(rbsp);
    ExpGolombStats stats;
    ExpGolombDecoder decoder(reader, stats);
    Diagnostic diag;

    for (std::uint32_t expected : ue_values) {
        std::uint64_t value = 0;
        CHECK(decoder.read_ue(value, &diag));
        CHECK_EQ(value, static_cast<std::uint64_t>(expected));
    }
    for (std::int32_t expected : se_values) {
        std::int64_t value = 0;
        CHECK(decoder.read_se(value, &diag));
        CHECK_EQ(value, static_cast<std::int64_t>(expected));
    }
    CHECK_EQ(stats.ue_calls, static_cast<std::uint64_t>(ue_values.size()));
    CHECK_EQ(stats.se_calls, static_cast<std::uint64_t>(se_values.size()));
    CHECK_EQ(stats.ue_failures, std::uint64_t{0});
    CHECK_EQ(stats.se_failures, std::uint64_t{0});
    // ue(1000) is 19 bits; se(128) codes 256 -> 17 bits.
    CHECK_EQ(stats.max_ue_bits, std::uint32_t{19});
    CHECK_EQ(stats.max_se_bits, std::uint32_t{17});
    CHECK_EQ(stats.max_leading_zeros, std::uint32_t{9});
    CHECK_EQ(stats.max_ue_value, std::uint64_t{1000});
    CHECK_EQ(stats.max_se_magnitude, std::uint64_t{129});
    CHECK(reader.read_rbsp_trailing_bits(&diag));
}

void test_exp_golomb_truncated_counts_failure() {
    // A bare stop bit is a valid ue(0); a stop bit before enough suffix bits
    // is truncation. Here four leading zeros need four suffix bits but the
    // payload ends after three.
    RbspBuffer rbsp;
    rbsp.bytes = {0b0000'1000};
    BitReader reader(rbsp);
    ExpGolombStats stats;
    ExpGolombDecoder decoder(reader, stats);
    Diagnostic diag;
    std::uint64_t value = 42;
    CHECK(!decoder.read_ue(value, &diag));
    CHECK_EQ(value, std::uint64_t{42});
    CHECK(diag.kind == ErrorKind::Truncated);
    CHECK_EQ(stats.ue_calls, std::uint64_t{1});
    CHECK_EQ(stats.ue_failures, std::uint64_t{1});

    // Five leading zeros need five suffix bits but only four are present.
    RbspBuffer short_rbsp;
    short_rbsp.bytes = {0b0000'0100};
    BitReader short_reader(short_rbsp);
    ExpGolombStats short_stats;
    ExpGolombDecoder short_decoder(short_reader, short_stats);
    CHECK(!short_decoder.read_ue(value, &diag));
    CHECK(diag.kind == ErrorKind::Truncated);
    CHECK_EQ(short_stats.ue_failures, std::uint64_t{1});
}

void test_exp_golomb_limits() {
    // 34 leading zeros exceeds the default 32-zero limit.
    selftest::BitWriter writer;
    writer.bits(0, 34);
    writer.bit(true);
    RbspBuffer rbsp;
    rbsp.bytes = writer.bytes();
    BitReader reader(rbsp);
    ExpGolombStats stats;
    ExpGolombDecoder decoder(reader, stats);
    Diagnostic diag;
    std::uint64_t value = 7;
    CHECK(!decoder.read_ue(value, &diag));
    CHECK_EQ(value, std::uint64_t{7});
    CHECK(diag.kind == ErrorKind::ExpGolomb);
    CHECK_EQ(stats.ue_failures, std::uint64_t{1});
    CHECK_EQ(stats.max_leading_zeros, std::uint32_t{0});

    // The largest permitted codeword decodes and is recorded.
    selftest::BitWriter big;
    big.ue(0xFFFFFFFFu);
    RbspBuffer big_rbsp;
    big_rbsp.bytes = big.bytes();
    BitReader big_reader(big_rbsp);
    ExpGolombStats big_stats;
    ExpGolombDecoder big_decoder(big_reader, big_stats);
    std::uint64_t big_value = 0;
    CHECK(big_decoder.read_ue(big_value, &diag));
    CHECK_EQ(big_value, std::uint64_t{0xFFFFFFFFu});
    // Largest permitted codeword: code 2^32, 32 leading zeros, 65 bits total.
    CHECK_EQ(big_stats.max_ue_bits, std::uint32_t{65});
    CHECK_EQ(big_stats.max_leading_zeros, std::uint32_t{32});
}

void test_exp_golomb_u32_overflow() {
    // Widen the limits so a 32/33-leading-zero codeword is structurally legal;
    // the u32 wrapper must still reject the one that does not fit.
    ExpGolombLimits wide;
    wide.max_leading_zeros = 33;
    wide.max_codeword_bits = 67;

    selftest::BitWriter writer;
    writer.ue(0xFFFFFFFEu);     // code 0xFFFFFFFF, 31 leading zeros, fits u32
    writer.ue(0x100000000ull);  // code 0x100000001, 32 leading zeros, too wide
    RbspBuffer rbsp;
    rbsp.bytes = writer.bytes();
    BitReader reader(rbsp);
    ExpGolombStats stats;
    ExpGolombDecoder decoder(reader, stats, wide);
    Diagnostic diag;
    std::uint32_t value = 0;
    CHECK(decoder.read_ue_u32(value, &diag));
    CHECK_EQ(value, std::uint32_t{0xFFFFFFFEu});
    CHECK(!decoder.read_ue_u32(value, &diag));
    CHECK_EQ(value, std::uint32_t{0xFFFFFFFEu});
    CHECK(diag.kind == ErrorKind::ExpGolomb);
    CHECK_EQ(stats.ue_calls, std::uint64_t{2});
    CHECK_EQ(stats.ue_failures, std::uint64_t{1});
}

void test_exp_golomb_stats_merge() {
    ExpGolombStats a;
    a.ue_calls = 3;
    a.se_calls = 2;
    a.ue_failures = 1;
    a.max_ue_bits = 9;
    a.max_se_magnitude = 4;
    ExpGolombStats b;
    b.ue_calls = 4;
    b.se_calls = 1;
    b.se_failures = 2;
    b.max_ue_bits = 13;
    b.max_ue_value = 99;
    a.merge(b);
    CHECK_EQ(a.ue_calls, std::uint64_t{7});
    CHECK_EQ(a.se_calls, std::uint64_t{3});
    CHECK_EQ(a.ue_failures, std::uint64_t{1});
    CHECK_EQ(a.se_failures, std::uint64_t{2});
    CHECK_EQ(a.max_ue_bits, std::uint32_t{13});
    CHECK_EQ(a.max_ue_value, std::uint64_t{99});
    CHECK_EQ(a.max_se_magnitude, std::uint64_t{4});
}

}  // namespace

REGISTER_TEST(test_exp_golomb_round_trip);
REGISTER_TEST(test_exp_golomb_truncated_counts_failure);
REGISTER_TEST(test_exp_golomb_limits);
REGISTER_TEST(test_exp_golomb_u32_overflow);
REGISTER_TEST(test_exp_golomb_stats_merge);
