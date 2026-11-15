// Catch2 entry point + fixture-generation coverage. The actual synthetic
// H.264 streams are produced by h264::FixtureWriter (src/fixture_writer.*),
// so every sample used in these tests is generated from code.
#define CATCH_CONFIG_MAIN
#include "catch_amalgamated.hpp"

#include "bit_reader.hpp"
#include "fixture_writer.hpp"

#include <algorithm>

using namespace h264;

TEST_CASE("fixture writer generates all required stream shapes",
          "[fixture]") {
    FixtureWriter fw;

    auto sample = fw.buildSample();
    REQUIRE(sample.size() > 16);
    // Every fixture stream is Annex B framed and starts with a start code
    // (either 00 00 01 or the 4-byte 00 00 00 01 form).
    REQUIRE(sample[0] == 0x00);
    REQUIRE(sample[1] == 0x00);
    bool hasStartCode = (sample[2] == 0x01) ||
                        (sample[2] == 0x00 && sample[3] == 0x01);
    REQUIRE(hasStartCode);

    auto avcc = fw.buildSampleAvcc();
    REQUIRE(avcc.size() > 8);
    // Length-prefixed: no start code at the head.
    bool startsWithCode = avcc[0] == 0 && avcc[1] == 0 && avcc[2] == 1;
    REQUIRE_FALSE(startsWithCode);

    auto truncated = fw.buildTruncatedNal();
    REQUIRE(truncated.size() >= 5);

    auto badGolomb = fw.buildBadExpGolomb();
    REQUIRE(badGolomb.size() > truncated.size());

    auto unknownType = fw.buildUnknownNalType();
    // Adds one extra NAL on top of the base sample.
    REQUIRE(unknownType.size() > sample.size());
    REQUIRE(unknownType.size() >= sample.size() + 6);

    auto updated = fw.buildMidStreamSpsUpdate();
    REQUIRE(updated.size() > sample.size());
}

TEST_CASE("RBSP emulation prevention round trip", "[fixture][rbsp]") {
    // Payload containing patterns that must be escaped and restored.
    std::vector<std::uint8_t> rbsp = {
        0x00, 0x00, 0x00, 0x00, 0x01, 0x03, 0x00, 0x00, 0x02, 0xFF};
    auto ebsp = rbspEscape(rbsp);
    REQUIRE(ebsp.size() >= rbsp.size());

    bool truncated = true;
    auto restored = rbspUnescape(ebsp.data(), ebsp.size(), &truncated);
    REQUIRE_FALSE(truncated);
    REQUIRE(restored == rbsp);

    // A trailing 00 00 03 is an incomplete escape sequence.
    std::vector<std::uint8_t> dangling = {0x12, 0x00, 0x00, 0x03};
    truncated = false;
    auto part = rbspUnescape(dangling.data(), dangling.size(), &truncated);
    REQUIRE(truncated);
    REQUIRE(part == std::vector<std::uint8_t>{0x12, 0x00, 0x00});
}
