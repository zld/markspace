#include <catch2/catch_test_macros.hpp>

#include "bit_reader.hpp"

#include <vector>

using namespace h264;

TEST_CASE("BitReader reads big-endian bits and bytes", "[bit_reader]") {
    const std::vector<std::uint8_t> data = {0b10110010, 0b00001111};
    BitReader br(data, 42);
    REQUIRE(br.byte_offset() == 42);
    REQUIRE(br.read_bit() == 1);
    REQUIRE(br.read_bit() == 0);
    REQUIRE(br.read_bits(3) == 0b110);  // bits 2..4
    REQUIRE(br.bit_position() == 5);
    REQUIRE_FALSE(br.byte_aligned());
    // Remaining 11 bits: 010 (tail of byte 0) then 00001111 (byte 1).
    REQUIRE(br.read_bits(11) == 0b01000001111);
    REQUIRE(br.eof());
    REQUIRE(br.byte_aligned());
}

TEST_CASE("BitReader throws with offset on over-read", "[bit_reader]") {
    const std::vector<std::uint8_t> data = {0xFF};
    BitReader br(data, 7);
    REQUIRE(br.read_bits(8) == 0xFF);
    REQUIRE_THROWS_AS(br.read_bit(), ParseError);
    try {
        br.read_bits(1);
        FAIL("expected ParseError");
    } catch (const ParseError& e) {
        REQUIRE(e.byte_offset() == 7);
    }
}

TEST_CASE("BitReader rejects impossible widths", "[bit_reader]") {
    const std::vector<std::uint8_t> data(8, 0);
    BitReader br(data, 0);
    REQUIRE(br.read_bits(0) == 0);
    REQUIRE_THROWS_AS(br.read_bits(33), ParseError);
}
