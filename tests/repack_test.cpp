#include <catch2/catch_test_macros.hpp>

#include <filesystem>

#include "fmp4/fixture.hpp"
#include "fmp4/fragment_index.hpp"
#include "fmp4/repacker.hpp"

using namespace fmp4;
namespace fs = std::filesystem;

namespace {

const fs::path& fixture_root() {
  static const fs::path root = [] {
    fs::path p = fs::temp_directory_path() / "fmp4-repack-test";
    std::error_code ec;
    fs::remove_all(p, ec);
    write_fixture(p.string());
    return p;
  }();
  return root;
}

RepackOptions make_opts(const std::string& set, const std::string& out_name,
                        int64_t tolerance = 0) {
  RepackOptions opt;
  opt.init_path = (fixture_root() / "init.mp4").string();
  opt.segments_dir = (fixture_root() / set).string();
  opt.out_dir = (fs::temp_directory_path() / out_name).string();
  opt.first_seq = 41;
  opt.last_seq = 44;
  opt.max_decode_delta_ticks = tolerance;
  opt.report_path = opt.out_dir + "/report.json";
  std::error_code ec;
  fs::remove_all(opt.out_dir, ec);
  return opt;
}

std::string read_text(const std::string& path) {
  auto bytes = read_file(path);
  return std::string(bytes.begin(), bytes.end());
}

}  // namespace

TEST_CASE("repack of good segments succeeds with zero deltas") {
  RepackOptions opt = make_opts("segments-good", "fmp4-out-good");
  CHECK(run_repack(opt) == 0);
  std::string js = read_text(opt.report_path);
  CHECK(js.find("\"before_fragments\": 4") != std::string::npos);
  CHECK(js.find("\"after_fragments\": 4") != std::string::npos);
  CHECK(js.find("\"before_moof_contiguous\": true") != std::string::npos);
  CHECK(js.find("\"after_moof_contiguous\": true") != std::string::npos);
  CHECK(js.find("\"before_track_decode_delta_ticks\": [{\"track_id\": 1, "
                "\"decode_delta_ticks\": [0, 0, 0]}") != std::string::npos);
  CHECK(js.find("\"after_track_decode_delta_ticks\"") != std::string::npos);
}

TEST_CASE("repacked output reparses and preserves media bytes") {
  RepackOptions opt = make_opts("segments-good", "fmp4-out-reparse");
  REQUIRE(run_repack(opt) == 0);
  std::string out_file = opt.out_dir + "/repacked.mp4";
  auto idx = build_segment_index(out_file);
  REQUIRE(idx.fragments.size() == 4);
  for (size_t i = 0; i < 4; ++i)
    CHECK(idx.fragments[i].sequence == 41 + i);
  // Media sample bytes must be identical to the input mdat payloads.
  auto in = build_segment_index((fixture_root() / "segments-good" /
                                 "seg-0041.mp4").string());
  auto in_bytes = read_file(in.file);
  auto out_bytes = read_file(out_file);
  const Fragment& fi = in.fragments[0];
  const Fragment& fo = idx.fragments[0];
  REQUIRE(fi.mdat_size == fo.mdat_size);
  CHECK(std::equal(in_bytes.begin() + fi.mdat_offset,
                   in_bytes.begin() + fi.mdat_offset + fi.mdat_size,
                   out_bytes.begin() + fo.mdat_offset));
  // Data offsets are rewritten to the adjacent moof+mdat layout.
  REQUIRE(fo.trafs[0].truns[0].data_offset.has_value());
  CHECK(*fo.trafs[0].truns[0].data_offset ==
        static_cast<int32_t>(fo.moof_size + 8));
}

TEST_CASE("missing sequence reports exit code 4") {
  RepackOptions opt = make_opts("segments-missing", "fmp4-out-missing");
  CHECK(run_repack(opt) == kExitSequence);
  CHECK_FALSE(fs::exists(opt.out_dir + "/repacked.mp4"));
}

TEST_CASE("corrupt segment reports exit code 3") {
  RepackOptions opt = make_opts("segments-corrupt", "fmp4-out-corrupt");
  opt.first_seq = 41;
  opt.last_seq = 41;
  CHECK(run_repack(opt) == kExitFormat);
}

TEST_CASE("decode delta beyond tolerance reports exit code 6") {
  RepackOptions strict = make_opts("segments-delta", "fmp4-out-delta0", 0);
  CHECK(run_repack(strict) == kExitDelta);
  RepackOptions loose = make_opts("segments-delta", "fmp4-out-delta500", 500);
  CHECK(run_repack(loose) == 0);
}

TEST_CASE("duplicate sequence reports exit code 4") {
  fs::path dup = fs::temp_directory_path() / "fmp4-dup-segments";
  std::error_code ec;
  fs::remove_all(dup, ec);
  fs::create_directories(dup);
  fs::copy_file(fixture_root() / "segments-good" / "seg-0041.mp4",
                dup / "a.mp4");
  fs::copy_file(fixture_root() / "segments-good" / "seg-0041.mp4",
                dup / "b.mp4");
  RepackOptions opt = make_opts("segments-good", "fmp4-out-dup");
  opt.segments_dir = dup.string();
  opt.first_seq = 41;
  opt.last_seq = 41;
  CHECK(run_repack(opt) == kExitSequence);
  fs::remove_all(dup, ec);
}

TEST_CASE("incompatible init segment reports exit code 5") {
  RepackOptions opt = make_opts("segments-good", "fmp4-out-badinit");
  opt.init_path = (fixture_root() / "init-bad.mp4").string();
  CHECK(run_repack(opt) == kExitInit);
}

TEST_CASE("no half-written output is left behind on failure") {
  RepackOptions opt = make_opts("segments-delta", "fmp4-out-clean", 0);
  CHECK(run_repack(opt) == kExitDelta);
  CHECK_FALSE(fs::exists(opt.out_dir + "/repacked.mp4"));
  CHECK_FALSE(fs::exists(opt.out_dir + "/.repacked.tmp"));
}
