#include "catch_amalgamated.hpp"

#include "bit_reader.hpp"
#include "exp_golomb.hpp"
#include "fixture_writer.hpp"
#include "group_index.hpp"
#include "h264_parser.hpp"
#include "nal_scanner.hpp"

#include <cstdint>
#include <vector>

using namespace h264;

namespace {

struct StreamArtifacts {
    std::vector<std::uint8_t> bytes;
    ScanResult scan;
    H264Parser parser;
    GroupIndex index;
};

StreamArtifacts analyze(const std::vector<std::uint8_t>& bytes,
                        const std::string& hint = "auto",
                        bool tolerant = true) {
    StreamArtifacts a;
    a.bytes = bytes;
    a.scan = scanNals(bytes.data(), bytes.size(), hint, tolerant);
    for (const auto& nal : a.scan.nals)
        a.parser.feed(nal, bytes.data(), tolerant);
    a.index = buildGroupIndex(a.scan, a.parser);
    return a;
}

} // namespace

// ---------- BitReader / Exp-Golomb fundamentals ----------

TEST_CASE("bounded bit reader honors truncation", "[bitreader]") {
    std::uint8_t data[] = {0xAB, 0xCD};
    BitReader br(data, sizeof(data));
    REQUIRE(br.readBits(8) == 0xAB);
    REQUIRE(br.readBits(8) == 0xCD);
    REQUIRE(br.error() == false);
    REQUIRE(br.readBits(1) == 0);
    REQUIRE(br.error() == true);

    BitReader br2(data, sizeof(data));
    REQUIRE(br2.readBits(33) == 0);
    REQUIRE(br2.error());
}

TEST_CASE("exp-golomb ue/se decoding with limit", "[expgolomb]") {
    // Encode a known sequence with the project writer so the expected
    // bitstream is derived rather than hand-assembled.
    BitWriter w;
    w.writeUe(0); w.writeUe(1); w.writeUe(2); w.writeUe(3);
    w.rbspStop();
    BitReader br(w.bytes().data(), w.bytes().size());
    REQUIRE(decodeUe(br) == 0);
    REQUIRE(decodeUe(br) == 1);
    REQUIRE(decodeUe(br) == 2);
    REQUIRE(decodeUe(br) == 3);

    // Signed mapping: codeNum 0..3 -> 0, +1, -1, +2.
    BitWriter sw;
    sw.writeUe(0); sw.writeUe(1); sw.writeUe(2); sw.writeUe(3);
    sw.rbspStop();
    BitReader sbr(sw.bytes().data(), sw.bytes().size());
    REQUIRE(decodeSe(sbr) == 0);   // codeNum 0
    REQUIRE(decodeSe(sbr) == 1);   // codeNum 1 -> +1
    REQUIRE(decodeSe(sbr) == -1);  // codeNum 2 -> -1
    REQUIRE(decodeSe(sbr) == 2);   // codeNum 3 -> +2

    // 32 leading zeros exceed the 31-zero limit.
    std::vector<std::uint8_t> zeros(8, 0x00);
    BitReader bad(zeros.data(), zeros.size());
    REQUIRE(decodeUe(bad, 31) == 0);
    REQUIRE(bad.error());
}

// ---------- NAL scanner ----------

TEST_CASE("auto-detect Annex B and AVCC", "[scanner]") {
    FixtureWriter fw;
    auto annexB = fw.buildSample();
    auto a = analyze(annexB);
    REQUIRE(a.scan.format == StreamFormat::AnnexB);
    REQUIRE(a.scan.errors.empty());
    REQUIRE(a.scan.nals.size() >= 12);
    REQUIRE(a.scan.nals.size() <= 20);

    auto avccBytes = fw.buildSampleAvcc();
    auto v = analyze(avccBytes);
    REQUIRE(v.scan.format == StreamFormat::LengthPrefixed);
    REQUIRE(v.scan.errors.empty());
    REQUIRE(v.scan.nals.size() == a.scan.nals.size());
}

TEST_CASE("format hints conflicting with data are rejected", "[scanner]") {
    FixtureWriter fw;
    auto annexB = fw.buildSample();
    auto r = scanNals(annexB.data(), annexB.size(), "avcc", true);
    REQUIRE(r.format == StreamFormat::Unknown);
    REQUIRE_FALSE(r.errors.empty());

    auto avcc = fw.buildSampleAvcc();
    r = scanNals(avcc.data(), avcc.size(), "annexb", true);
    REQUIRE_FALSE(r.errors.empty());
}

TEST_CASE("empty input and undetectable format reported with offset",
          "[scanner][errors]") {
    std::vector<std::uint8_t> empty;
    auto r = scanNals(empty.data(), 0, "auto", true);
    REQUIRE(r.nals.empty());
    REQUIRE(r.errors.size() == 1);
    REQUIRE(r.errors.front().offset == 0);

    std::vector<std::uint8_t> garbage = {0xAA, 0xBB, 0xCC, 0xDD,
                                         0xEE, 0xFF, 0x11};
    r = scanNals(garbage.data(), garbage.size(), "auto", true);
    REQUIRE(r.format == StreamFormat::Unknown);
    REQUIRE_FALSE(r.errors.empty());
}

TEST_CASE("unknown NAL unit type is classified", "[scanner][errors]") {
    FixtureWriter fw;
    auto bytes = fw.buildUnknownNalType();
    auto tolerant = analyze(bytes, "auto", true);
    REQUIRE_FALSE(tolerant.scan.errors.empty());
    // Tolerant mode keeps scanning and still reaches the trailing IDR.
    REQUIRE(tolerant.index.totals.i >= 2);

    auto strict = scanNals(bytes.data(), bytes.size(), "auto", false);
    REQUIRE_FALSE(strict.errors.empty());
}

// ---------- SPS / PPS parsing ----------

TEST_CASE("SPS fields, cropping and timing are parsed", "[parser][sps]") {
    FixtureWriter fw;
    auto a = analyze(fw.buildSample());
    const SpsData* sps = a.parser.activeSpsForOffset(a.bytes.size());
    REQUIRE(sps != nullptr);
    REQUIRE(sps->profileIdc == 66);
    REQUIRE(sps->levelIdc == 30);
    REQUIRE(sps->widthMbUnits == 40);
    REQUIRE(sps->heightMapUnits == 12);
    REQUIRE(sps->frameWidth == 640);
    REQUIRE(sps->frameHeight == 384);      // uncropped
    REQUIRE(sps->croppedWidth == 640);
    REQUIRE(sps->croppedHeight == 360);    // cropped
    REQUIRE_FALSE(sps->frameMbsOnlyFlag);
    REQUIRE(sps->timingInfoPresent);
    REQUIRE(sps->timeScale == 50);
    REQUIRE(sps->numUnitsInTick == 1);
    REQUIRE(sps->frameRate == Catch::Approx(25.0));
}

TEST_CASE("PPS references SPS and missing reference is located",
          "[parser][pps]") {
    FixtureWriter fw;
    auto a = analyze(fw.buildSample());
    REQUIRE(a.parser.ppsHistory().count(0) == 1);
    REQUIRE(a.parser.ppsHistory().at(0).back().data.spsId == 0);
    REQUIRE(a.parser.errors().empty());

    // PPS-only stream: the SPS reference cannot be resolved.
    BitWriter w;
    w.writeUe(0); w.writeUe(7); // pps id 0 -> unknown sps id 7
    w.writeBits(0, 1); w.writeBits(0, 1);
    w.writeUe(0); w.writeUe(0); w.writeUe(0);
    w.writeBits(0, 1); w.writeBits(0, 2);
    w.writeSe(0); w.writeSe(0); w.writeSe(0);
    w.writeBits(1, 1); w.writeBits(0, 1); w.writeBits(0, 1);
    w.rbspStop();
    std::vector<std::uint8_t> stream;
    const std::uint8_t sc[4] = {0, 0, 0, 1};
    stream.insert(stream.end(), sc, sc + 4);
    stream.push_back(0x68);
    auto ebsp = rbspEscape(w.bytes());
    stream.insert(stream.end(), ebsp.begin(), ebsp.end());

    auto broken = analyze(stream);
    bool found = false;
    for (const auto& e : broken.parser.errors())
        if (e.stage == "pps" && e.reason.find("SPS id 7") !=
            std::string::npos) found = true;
    REQUIRE(found);
    bool eventFound = false;
    for (const auto& ev : broken.parser.events())
        if (ev.kind == EventKind::PpsMissingSps) eventFound = true;
    REQUIRE(eventFound);
}

TEST_CASE("mid-stream SPS update keeps history and emits event",
          "[parser][events]") {
    FixtureWriter fw;
    auto a = analyze(fw.buildMidStreamSpsUpdate());
    const auto& hist = a.parser.spsHistory().at(0);
    REQUIRE(hist.size() == 2);
    REQUIRE(hist[0].data.levelIdc == 30);
    REQUIRE(hist[1].data.levelIdc == 31);
    REQUIRE(hist[1].version == 1);
    REQUIRE(hist[0].firstOffset == hist[1].firstOffset);
    bool updated = false, duplicated = false;
    for (const auto& ev : a.parser.events()) {
        if (ev.kind == EventKind::SpsUpdate) updated = true;
        if (ev.kind == EventKind::SpsDuplicate) duplicated = true;
    }
    REQUIRE(updated);
    REQUIRE_FALSE(duplicated);
}

// ---------- Access units, frame types and groups ----------

TEST_CASE("multi-slice access units aggregate into one frame",
          "[grouping]") {
    FixtureWriter fw;
    auto a = analyze(fw.buildSample());
    // 3 IDR slices + P + B + P + 2 field slices + trailing IDR = 9 slices,
    // but 6 access units.
    REQUIRE(a.parser.slices().size() == 9);
    REQUIRE(a.index.accessUnits.size() == 6);
    // Invariant: frame-type counts cover every access unit exactly once.
    REQUIRE(a.index.totals.total() ==
            static_cast<int>(a.index.accessUnits.size()));
    // First AU has three slices.
    REQUIRE(a.index.accessUnits.front().sliceCount == 3);
    // Field pair is one AU flagged interlaced.
    bool hasFieldAu = false;
    for (const auto& au : a.index.accessUnits)
        if (au.interlaced) hasFieldAu = true;
    REQUIRE(hasFieldAu);
}

TEST_CASE("frame type counts and group boundaries", "[grouping]") {
    FixtureWriter fw;
    auto a = analyze(fw.buildSample());
    REQUIRE(a.index.totals.i == 2);
    REQUIRE(a.index.totals.p == 3);
    REQUIRE(a.index.totals.b == 1);
    REQUIRE(a.index.totals.unknown == 0);
    REQUIRE(a.index.groups.size() == 2);
    for (const auto& g : a.index.groups) {
        REQUIRE(g.startOffset < a.bytes.size());
        REQUIRE(g.frameCount >= 1);
    }
    REQUIRE(a.index.groups.front().hasLeadingNonVcl);
    REQUIRE(a.index.groups.back().openedByIdr);
    // Group offsets are strictly increasing.
    REQUIRE(a.index.groups[1].startOffset >
            a.index.groups[0].startOffset);
}

TEST_CASE("unknown slice syntax values map to unknown", "[parser]") {
    REQUIRE(mapSliceType(0) == FT_P);
    REQUIRE(mapSliceType(1) == FT_B);
    REQUIRE(mapSliceType(2) == FT_I);
    REQUIRE(mapSliceType(3) == FT_UNKNOWN);
    REQUIRE(mapSliceType(4) == FT_UNKNOWN);
    REQUIRE(mapSliceType(9) == FT_UNKNOWN);
    // Values 5..9 encode the same types 0..4 per H.264 Table 7-6.
    FixtureWriter fw;
    auto a = analyze(fw.buildSample());
    for (const auto& sh : a.parser.slices())
        if (sh.parseOk) REQUIRE(sh.frameType != FT_UNKNOWN);
}

// ---------- Fault tolerance / strict mode ----------

TEST_CASE("truncated NAL is located and reported", "[errors]") {
    FixtureWriter fw;
    auto bytes = fw.buildTruncatedNal();
    auto tolerant = analyze(bytes, "auto", true);
    REQUIRE_FALSE(tolerant.parser.errors().empty());
    bool trunc = false;
    for (const auto& e : tolerant.parser.errors())
        if (e.reason.find("truncated") != std::string::npos) trunc = true;
    REQUIRE(trunc);
    // The broken slice still yields an access unit, typed unknown.
    REQUIRE(tolerant.index.totals.unknown >= 1);
}

TEST_CASE("illegal Exp-Golomb stops strict, tolerated otherwise",
          "[errors][strict]") {
    FixtureWriter fw;
    auto bytes = fw.buildBadExpGolomb();
    auto tolerant = analyze(bytes, "auto", true);
    REQUIRE_FALSE(tolerant.parser.errors().empty());

    auto strict = analyze(bytes, "auto", false);
    bool golombError = false;
    for (const auto& e : strict.parser.errors())
        if (e.stage == "slice") golombError = true;
    REQUIRE(golombError);
}

TEST_CASE("skipped count and first error offset are exposed",
          "[errors]") {
    FixtureWriter fw;
    auto bytes = fw.buildTruncatedNal();
    auto a = analyze(bytes, "auto", true);
    REQUIRE(a.index.skippedNals >= 1);
    REQUIRE(a.index.firstErrorOffset !=
            static_cast<std::size_t>(-1));
    REQUIRE(a.index.firstErrorOffset < bytes.size());
}

TEST_CASE("clean sample reports zero errors", "[smoke]") {
    FixtureWriter fw;
    auto a = analyze(fw.buildSample());
    REQUIRE(a.scan.errors.empty());
    REQUIRE(a.parser.errors().empty());
    REQUIRE(a.index.firstErrorOffset == static_cast<std::size_t>(-1));
}
