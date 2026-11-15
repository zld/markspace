#include "catch_amalgamated.hpp"

#include "bit_reader.hpp"
#include "exp_golomb.hpp"

#include <cstdint>
#include <vector>

using namespace h264;

// Initial smoke tests for the lowest layer; full structural coverage is
// added in the follow-up commit.
TEST_CASE("bounded bit reader honors truncation", "[bitreader]") {
    std::uint8_t data[] = {0xAB, 0xCD};
    BitReader br(data, sizeof(data));
    REQUIRE(br.readBits(8) == 0xAB);
    REQUIRE(br.readBits(8) == 0xCD);
    REQUIRE_FALSE(br.error());
    REQUIRE(br.readBits(1) == 0);
    REQUIRE(br.error());
}

TEST_CASE("exp-golomb leading-zero cap", "[expgolomb]") {
    std::vector<std::uint8_t> zeros(8, 0x00);
    BitReader bad(zeros.data(), zeros.size());
    REQUIRE(decodeUe(bad, 31) == 0);
    REQUIRE(bad.error());
}
