#include <catch2/catch_test_macros.hpp>

#include <filesystem>

#include "fmp4/fixture.hpp"
#include "fmp4/fragment_index.hpp"
#include "fmp4/init_compat.hpp"

using namespace fmp4;
namespace fs = std::filesystem;

namespace {

const fs::path& fixture_root() {
  static const fs::path root = [] {
    fs::path p = fs::temp_directory_path() / "fmp4-index-test";
    std::error_code ec;
    fs::remove_all(p, ec);
    write_fixture(p.string());
    return p;
  }();
  return root;
}

std::string seg(const std::string& set, uint32_t seq) {
  char name[64];
  std::snprintf(name, sizeof(name), "seg-%04u.mp4", seq);
  return (fixture_root() / set / name).string();
}

}  // namespace

TEST_CASE("init segment parses tracks, timescales and trex defaults") {
  InitInfo init = parse_init((fixture_root() / "init.mp4").string());
  REQUIRE(init.tracks.size() == 2);
  CHECK(init.tracks[0].track_id == 1);
  CHECK(init.tracks[0].timescale == 90000);
  CHECK(init.tracks[0].has_trex);
  CHECK(init.tracks[0].trex.default_sample_duration == 3000);
  CHECK(init.tracks[1].track_id == 2);
  CHECK(init.tracks[1].timescale == 48000);
  CHECK(init.tracks[1].stsd_entry_count == 1);
  CHECK(validate_init(init).empty());
}

TEST_CASE("init segment with zero timescale is incompatible") {
  InitInfo init = parse_init((fixture_root() / "init-bad.mp4").string());
  auto issues = validate_init(init);
  REQUIRE(issues.size() == 1);
  CHECK(issues[0].track_id == 2);
  CHECK(issues[0].field == "timescale");
}

TEST_CASE("segment index reads mfhd sequence and mixed box versions") {
  auto idx = build_segment_index(seg("segments-good", 42));
  REQUIRE(idx.fragments.size() == 1);
  const Fragment& f = idx.fragments[0];
  CHECK(f.sequence == 42);
  REQUIRE(f.trafs.size() == 2);
  REQUIRE(f.trafs[0].tfdt.has_value());
  CHECK(f.trafs[0].tfdt->version == 1);
  CHECK(f.trafs[0].tfdt->value == 9000);
  CHECK(f.trafs[1].tfdt->value == 4096);
  REQUIRE(f.trafs[0].truns.size() == 1);
  CHECK(f.trafs[0].truns[0].version == 1);
  // trun v1 carries signed composition offsets.
  REQUIRE(f.trafs[0].truns[0].samples[0].cto.has_value());
  CHECK(*f.trafs[0].truns[0].samples[0].cto == -2);
}

TEST_CASE("tfdt version 0 parses as 32-bit decode time") {
  auto idx = build_segment_index(seg("segments-good", 41));
  REQUIRE(idx.fragments.size() == 1);
  CHECK(idx.fragments[0].trafs[0].tfdt->version == 0);
  // trun v0 carries unsigned composition offsets.
  CHECK(*idx.fragments[0].trafs[0].truns[0].samples[0].cto == 2);
}

TEST_CASE("sample duration falls back trun then tfhd then trex") {
  InitInfo init = parse_init((fixture_root() / "init.mp4").string());
  const TrexDefaults* trex1 = &init.tracks[0].trex;
  auto f41 = build_segment_index(seg("segments-good", 41)).fragments[0];
  CHECK(f41.trafs[0].truns[0].samples[0].duration.has_value());
  CHECK(traf_duration(f41.trafs[0], trex1, "") == 9000);
  auto f43 = build_segment_index(seg("segments-good", 43)).fragments[0];
  CHECK_FALSE(f43.trafs[0].truns[0].samples[0].duration.has_value());
  CHECK(f43.trafs[0].tfhd.default_sample_duration.has_value());
  CHECK(traf_duration(f43.trafs[0], trex1, "") == 9000);
  auto f44 = build_segment_index(seg("segments-good", 44)).fragments[0];
  CHECK_FALSE(f44.trafs[0].truns[0].samples[0].duration.has_value());
  CHECK_FALSE(f44.trafs[0].tfhd.default_sample_duration.has_value());
  CHECK(traf_duration(f44.trafs[0], trex1, "") == 9000);
  // Without any default the resolution must fail.
  Traf bare;
  bare.tfhd.track_id = 9;
  TrunSample s;
  CHECK_THROWS_AS(resolve_sample_duration(bare, s, nullptr, "mem", 0),
                  BoxError);
}

TEST_CASE("corrupt segment reports BoxError with file and offset") {
  try {
    build_segment_index(seg("segments-corrupt", 41));
    FAIL("expected BoxError");
  } catch (const BoxError& e) {
    CHECK(e.file.find("seg-0041.mp4") != std::string::npos);
    CHECK(e.offset == 0);
  }
}

TEST_CASE("size-0 box allowed only at end of file") {
  BoxWriter w;
  w.u32(0);
  w.fourcc("mdat");
  w.zeros(4);
  auto boxes = parse_boxes(w.buf.data(), w.buf.size(), "mem");
  REQUIRE(boxes.size() == 1);
  CHECK(boxes[0].size == 12);
  BoxWriter outer;
  size_t p = outer.begin_box("moov");
  outer.u32(0);
  outer.fourcc("mdat");
  outer.zeros(4);
  outer.end_box(p);
  auto top = parse_boxes(outer.buf.data(), outer.buf.size(), "mem");
  REQUIRE(top.size() == 1);
  CHECK_THROWS_AS(parse_children(outer.buf.data(), top[0], "mem"), BoxError);
}

TEST_CASE("extended 64-bit box size parses") {
  BoxWriter w;
  w.u32(1);
  w.fourcc("free");
  w.u64(24);
  w.zeros(8);
  auto boxes = parse_boxes(w.buf.data(), w.buf.size(), "mem");
  REQUIRE(boxes.size() == 1);
  CHECK(boxes[0].header_size == 16);
  CHECK(boxes[0].size == 24);
}

TEST_CASE("truncated and overflowing boxes report offsets") {
  BoxWriter w;
  w.u32(100);
  w.fourcc("mdat");
  w.zeros(4);
  try {
    parse_boxes(w.buf.data(), w.buf.size(), "mem");
    FAIL("expected BoxError");
  } catch (const BoxError& e) {
    CHECK(e.offset == 0);
  }
  BoxWriter x;
  x.u32(1);
  x.fourcc("free");
  x.u64(0xffffffffffffffffULL);  // huge extended size must not overflow
  x.zeros(8);
  CHECK_THROWS_AS(parse_boxes(x.buf.data(), x.buf.size(), "mem"), BoxError);
}

namespace {

void write_moof_with_trun(const fs::path& path, uint8_t trun_version,
                          uint32_t trun_flags) {
  BoxWriter w;
  size_t moof = w.begin_box("moof");
  size_t mfhd = w.begin_full_box("mfhd", 0, 0);
  w.u32(7);
  w.end_box(mfhd);
  size_t traf = w.begin_box("traf");
  size_t tfhd = w.begin_full_box("tfhd", 0, 0x20000);
  w.u32(1);
  w.end_box(tfhd);
  size_t tfdt = w.begin_full_box("tfdt", 0, 0);
  w.u32(0);
  w.end_box(tfdt);
  size_t trun = w.begin_full_box("trun", trun_version, trun_flags);
  w.u32(1);
  if (trun_flags & 0x200) w.u32(16);
  w.end_box(trun);
  w.end_box(traf);
  w.end_box(moof);
  size_t mdat = w.begin_box("mdat");
  w.zeros(16);
  w.end_box(mdat);
  write_file(path.string(), w.buf);
}

}  // namespace

TEST_CASE("unknown trun version and flags fail explicitly") {
  fs::path dir = fs::temp_directory_path() / "fmp4-badtrun";
  std::error_code ec;
  fs::create_directories(dir, ec);
  write_moof_with_trun(dir / "v9.mp4", 9, 0x200);
  CHECK_THROWS_AS(build_segment_index((dir / "v9.mp4").string()), BoxError);
  write_moof_with_trun(dir / "flags.mp4", 0, 0x200 | 0x20000);
  CHECK_THROWS_AS(build_segment_index((dir / "flags.mp4").string()), BoxError);
  fs::remove_all(dir, ec);
}
