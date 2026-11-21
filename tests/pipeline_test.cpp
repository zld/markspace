#include "fixture_builder.hpp"
#include "fragment_index.hpp"
#include "init_compat.hpp"
#include "repack.hpp"
#include "validator.hpp"

#include <catch2/catch_test_macros.hpp>

#include <filesystem>

using namespace mp4flow;
namespace fs = std::filesystem;

namespace {
struct Workspace {
  fs::path root;
  FixtureLayout layout;
  std::vector<InitSignature> inits() const {
    std::vector<InitSignature> out;
    for (const auto& p : layout.initFiles)
      out.push_back(extractInit(parseFile(p)));
    return out;
  }
  std::string media() const { return (root / "media").string(); }
};

Workspace makeWs(const std::string& tag, FixtureCase tc) {
  Workspace ws;
  ws.root = fs::temp_directory_path() / ("mp4flow-test-" + tag);
  fs::remove_all(ws.root);
  ws.layout = generateCase(ws.root.string(), tc);
  return ws;
}
} // namespace

TEST_CASE("valid case: split, renumber, rebase", "[pipeline]") {
  Workspace ws = makeWs("valid", FixtureCase::Valid);
  auto inits = ws.inits();
  InitCompatReport compat = compareInits(inits);
  CHECK(compat.compatible);
  REQUIRE(inits.at(0).tracks.size() == 2);
  CHECK(inits[0].tracks[0].timescale == 90000);
  CHECK(inits[0].tracks[1].timescale == 48000);

  FragmentIndex idx = buildIndex(ws.media(), inits);
  REQUIRE(idx.files.size() == 4);
  size_t moofs = 0;
  for (auto& f : idx.files) moofs += f.moofs.size();
  CHECK(moofs == 5);
  // Sequences cover 101..105; the last moof's tfdt rewinds to the 104 edge.
  CHECK(idx.conflicts.empty());
  CHECK(idx.ordinalConflicts.empty());
  const auto& last = idx.files.back().moofs.back();
  CHECK(last.sequence == 105);
  CHECK(last.tracks[0].decodeTime == 540000);
  validateSampleRanges(idx);

  RepackOptions opt;
  opt.mediaDir = ws.media();
  opt.outDir = (ws.root / "out").string();
  opt.maxCorrectionMs = 3000;
  RepackReport rep = repack(opt, inits, idx);
  CHECK(rep.inputFragments == 4);
  CHECK(rep.inputMoofs == 5);
  CHECK(rep.outputFragments == 5);
  CHECK(rep.outputMoofs == 5);
  CHECK(rep.firstOutSeq == 1);
  CHECK(rep.lastOutSeq == 5);
  CHECK(rep.continuous);
  REQUIRE(rep.tracks.size() == 2);
  CHECK(rep.tracks[0].maxAbsDecodeDeltaTicks == 180000);
  CHECK(rep.tracks[1].maxAbsDecodeDeltaTicks == 96000);

  // Published output must itself re-verify cleanly with the new 1..5 range.
  VerifyOptions vo;
  vo.mediaDir = opt.outDir;
  vo.hasExpectedRange = true;
  vo.expectFirst = 1;
  vo.expectLast = 5;
  VerifyReport vr = verifyMedia(vo, inits);
  CHECK(vr.ok);
  CHECK(vr.missing.empty());
}

TEST_CASE("gap case reports the exact missing sequence", "[pipeline]") {
  Workspace ws = makeWs("gap", FixtureCase::Gap);
  auto inits = ws.inits();
  VerifyOptions vo;
  vo.mediaDir = ws.media();
  vo.hasExpectedRange = true;
  vo.expectFirst = 101;
  vo.expectLast = 105;
  VerifyReport vr = verifyMedia(vo, inits);
  CHECK_FALSE(vr.ok);
  REQUIRE(vr.missing.size() == 1);
  CHECK(vr.missing[0] == 103);
}

TEST_CASE("incompatible init names track and field", "[pipeline]") {
  Workspace ws = makeWs("bad", FixtureCase::BadInit);
  auto inits = ws.inits();
  InitCompatReport rep = compareInits(inits);
  CHECK_FALSE(rep.compatible);
  bool sawTimescale = false;
  for (auto& d : rep.diffs) {
    if (d.field == "timescale") sawTimescale = true;
  }
  CHECK(sawTimescale);
}

TEST_CASE("encrypted sample entry is rejected", "[pipeline]") {
  Workspace ws = makeWs("enc", FixtureCase::Encrypted);
  auto inits = ws.inits();
  InitCompatReport rep = compareInits(inits);
  CHECK_FALSE(rep.compatible);
  CHECK_FALSE(rep.encryptedRejections.empty());
}

TEST_CASE("duplicate mfhd sequence is indexed and reported", "[pipeline]") {
  FixturePlan plan = makePlan(FixtureCase::Valid);
  plan.fragments.clear();
  plan.multiMoofJoinAt.clear();
  plan.fragments.push_back(FixtureFragment{101, {0, 0}, {2, 2}, 0, 0, 0});
  plan.fragments.push_back(FixtureFragment{101, {0, 0}, {2, 2}, 0, 0, 0});
  Workspace ws;
  ws.root = fs::temp_directory_path() / "mp4flow-test-dupseq";
  fs::remove_all(ws.root);
  ws.layout = generateFixture(ws.root.string(), plan);
  auto inits = ws.inits();
  FragmentIndex idx = buildIndex(ws.media(), inits);
  REQUIRE(idx.conflicts.size() == 1);
  CHECK(idx.conflicts[0].sequence == 101);
  // Repack tolerates duplicate mfhd sequences and renumbers 1..N.
  RepackOptions opt;
  opt.mediaDir = ws.media();
  opt.outDir = (ws.root / "out").string();
  RepackReport rep = repack(opt, inits, idx);
  CHECK(rep.outputMoofs == 2);
  CHECK(rep.firstOutSeq == 1);
  CHECK(rep.lastOutSeq == 2);
}

TEST_CASE("multi-moof file splits into separate outputs", "[pipeline]") {
  FixturePlan plan = makePlan(FixtureCase::Gap);
  plan.fragments.clear();
  plan.multiMoofJoinAt = {1, 2}; // three moofs, one physical file
  for (uint32_t s : {101u, 102u, 103u})
    plan.fragments.push_back(FixtureFragment{s, {0, 0}, {2, 2}, 0, 0, 0});
  Workspace ws;
  ws.root = fs::temp_directory_path() / "mp4flow-test-split";
  fs::remove_all(ws.root);
  ws.layout = generateFixture(ws.root.string(), plan);
  auto inits = ws.inits();
  FragmentIndex idx = buildIndex(ws.media(), inits);
  REQUIRE(idx.files.size() == 1);
  CHECK(idx.files[0].moofs.size() == 3);
  RepackOptions opt;
  opt.mediaDir = ws.media();
  opt.outDir = (ws.root / "out").string();
  RepackReport rep = repack(opt, inits, idx);
  CHECK(rep.inputMoofs == 3);
  CHECK(rep.outputFragments == 3);
  CHECK(fs::directory_iterator(opt.outDir) != fs::directory_iterator{});
}

TEST_CASE("signed composition offset via trun version 1", "[pipeline]") {
  FixturePlan plan = makePlan(FixtureCase::Valid);
  plan.fragments.resize(1);
  plan.fragments[0].trunVersion = 1;
  plan.fragments[0].cttsOffset = -100;
  Workspace ws;
  ws.root = fs::temp_directory_path() / "mp4flow-test-ctts";
  fs::remove_all(ws.root);
  ws.layout = generateFixture(ws.root.string(), plan);
  auto inits = ws.inits();
  FragmentIndex idx = buildIndex(ws.media(), inits);
  validateSampleRanges(idx);
  const auto& tf = idx.files[0].moofs[0].tracks[0];
  REQUIRE_FALSE(tf.samples.empty());
  CHECK(tf.samples[0].compositionOffset == -100);
}

TEST_CASE("tfdt version 0 is parsed as unsigned 32-bit", "[pipeline]") {
  FixturePlan plan = makePlan(FixtureCase::Valid);
  plan.fragments.resize(1);
  plan.fragments[0].tfdtVersion = 0;
  plan.fragments[0].tfdt = {42, 7};
  Workspace ws;
  ws.root = fs::temp_directory_path() / "mp4flow-test-tfdt0";
  fs::remove_all(ws.root);
  ws.layout = generateFixture(ws.root.string(), plan);
  auto inits = ws.inits();
  FragmentIndex idx = buildIndex(ws.media(), inits);
  const auto& m = idx.files[0].moofs[0];
  CHECK(m.tracks[0].tfdtPresent);
  CHECK(m.tracks[0].decodeTime == 42);
  CHECK(m.tracks[1].decodeTime == 7);
}
