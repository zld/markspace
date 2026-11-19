#include <catch2/catch_test_macros.hpp>

#include "bit_reader.hpp"
#include "bit_writer.hpp"
#include "exp_golomb.hpp"

#include <cstdint>
#include <vector>

using namespace h264;

namespace {
// Encode a ue value and wrap it in an RBSP byte buffer for read_ue.
std::vector<std::uint8_t> encode_ue(std::uint32_t v) {
    BitWriter w;
    w.write_ue(v);
    w.rbsp_trailing_bits();
    return w.take();
}
}  // namespace

TEST_CASE("unsigned Exp-Golomb round trips canonical code words",
          "[exp_golomb]") {
    const std::uint32_t values[] = {0, 1, 2, 3, 4, 5, 7, 8, 15, 16,
                                    100, 1023, 4096};
    for (std::uint32_t v : values) {
        auto buf = encode_ue(v);
        BitReader br(buf, 0);
        REQUIRE(read_ue(br) == v);
    }
}

TEST_CASE("signed Exp-Golomb maps code numbers per the spec",
          "[exp_golomb]") {
    struct Row { std::uint32_t code; std::int32_t signed_value; };
    const Row rows[] = {{0, 0}, {1, 1}, {2, -1}, {3, 2}, {4, -2},
                        {5, 3}, {6, -3}};
    for (const auto& r : rows) {
        BitWriter w;
        w.write_ue(r.code);
        w.rbsp_trailing_bits();
        auto buf = w.take();
        BitReader br(buf, 0);
        REQUIRE(read_se(br) == r.signed_value);
    }
}

TEST_CASE("truncated Exp-Golomb suffix reports a parse error",
          "[exp_golomb]") {
    // A ue with 4 leading zeros needs a 4-bit suffix. Place the terminating
    // one at the final bit of a one-byte buffer so no suffix bits exist.
    const std::vector<std::uint8_t> data = {0b00000001};
    BitReader br(data, 11);
    REQUIRE_THROWS_AS(read_ue(br), ParseError);
    try {
        BitReader br2(data, 11);
        read_ue(br2);
        FAIL("expected ParseError");
    } catch (const ParseError& e) {
        REQUIRE(e.byte_offset() == 11);
    }
}

TEST_CASE("truncated Exp-Golomb with no terminating one is rejected",
          "[exp_golomb]") {
    // Only leading zeros, never a 1 bit: hits EOF while counting zeros.
    const std::vector<std::uint8_t> data = {0x00, 0x00};
    BitReader br(data, 3);
    REQUIRE_THROWS_AS(read_ue(br), ParseError);
}

TEST_CASE("over-long leading zero run is rejected", "[exp_golomb]") {
    // 33 zero bits followed by a one exceeds the H.264 code-word limit.
    std::vector<std::uint8_t> data(5, 0x00);
    data[4] = 0b10000000;
    BitReader br(data, 5);
    REQUIRE_THROWS_AS(read_ue(br), ParseError);
}
