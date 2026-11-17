// Test harness for transition_lab. Each CTest entry runs one subcommand:
//   renderer_test <case> [scratch_dir]
#include <cstdio>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <string>

#include "biome.hpp"
#include "errors.hpp"
#include "frame_buffer.hpp"
#include "ppm.hpp"
#include "render_pipeline.hpp"
#include "timeline.hpp"
#include "transition.hpp"

#define CHECK(cond)                                              \
  do {                                                           \
    if (!(cond)) {                                               \
      std::fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond); \
      return 1;                                                  \
    }                                                            \
  } while (0)

namespace {

tl::RenderConfig demo_cfg() {
  tl::RenderConfig cfg;
  cfg.fps = tl::parse_rational("30000/1001", "fps");
  cfg.duration = tl::parse_rational("1/2", "duration");
  cfg.width = 96;
  cfg.height = 64;
  cfg.seed = 1337;
  return cfg;
}

int test_endpoints() {
  const tl::FrameBuffer from = tl::make_biome_frame(7, 48, 32, 0);
  const tl::FrameBuffer to = tl::make_biome_frame(7, 48, 32, 1);
  for (tl::TransitionType t :
       {tl::TransitionType::Fade, tl::TransitionType::Wipe,
        tl::TransitionType::Iris}) {
    tl::FrameBuffer out;
    tl::apply_transition(t, from, to, 0, 14, out);
    CHECK(out.pixels() == from.pixels());
    tl::apply_transition(t, from, to, 14, 14, out);
    CHECK(out.pixels() == to.pixels());
  }
  // Out-of-range progress must be rejected.
  tl::FrameBuffer out;
  try {
    tl::apply_transition(tl::TransitionType::Fade, from, to, 15, 14, out);
    return 1;
  } catch (const tl::ArgError&) {
  }
  return 0;
}

int test_determinism() {
  const tl::RenderConfig cfg = demo_cfg();
  const tl::TypeStats a = tl::render_sequence(cfg, tl::TransitionType::Fade, false);
  const tl::TypeStats b = tl::render_sequence(cfg, tl::TransitionType::Fade, false);
  CHECK(a.frame_hashes == b.frame_hashes);
  CHECK(a.first_hash == b.first_hash && a.last_hash == b.last_hash);
  CHECK(a.frames == 15);
  return 0;
}

int test_concurrency() {
  tl::RenderConfig cfg = demo_cfg();
  cfg.jobs = 1;
  const tl::TypeStats one =
      tl::render_sequence(cfg, tl::TransitionType::Wipe, false);
  cfg.jobs = 4;
  const tl::TypeStats four =
      tl::render_sequence(cfg, tl::TransitionType::Wipe, false);
  CHECK(one.frame_hashes == four.frame_hashes);
  CHECK(one.mid_changed_pixels == four.mid_changed_pixels);
  CHECK(one.mid_mean_abs_delta_milli == four.mid_mean_abs_delta_milli);
  return 0;
}

int write_text(const std::string& path, const std::string& text) {
  std::ofstream out(path, std::ios::binary | std::ios::trunc);
  out << text;
  return out ? 0 : 1;
}

int test_ppm(const std::string& scratch) {
  std::filesystem::create_directories(scratch);
  const tl::FrameBuffer fb = tl::make_biome_frame(3, 24, 16, 0);
  const std::string good = scratch + "/good.ppm";
  tl::write_ppm_atomic(good, fb);
  const tl::FrameBuffer back = tl::read_ppm_strict(good);
  CHECK(back.pixels() == fb.pixels());

  // Truncated payload -> DataError.
  {
    const std::string p = scratch + "/trunc.ppm";
    std::ofstream out(p, std::ios::binary | std::ios::trunc);
    out << "P6\n24 16\n255\n";
    const std::string half(fb.pixels().begin(), fb.pixels().begin() + 100);
    out.write(half.data(), static_cast<long>(half.size()));
    out.close();
    try {
      tl::read_ppm_strict(p);
      return 1;
    } catch (const tl::DataError&) {
    }
  }
  // maxval != 255 -> DataError.
  CHECK(write_text(scratch + "/maxval.ppm", "P6\n2 2\n254\n") == 0);
  try {
    tl::read_ppm_strict(scratch + "/maxval.ppm");
    return 1;
  } catch (const tl::DataError&) {
  }
  // Bad magic -> DataError.
  CHECK(write_text(scratch + "/magic.ppm", "P3\n2 2\n255\n") == 0);
  try {
    tl::read_ppm_strict(scratch + "/magic.ppm");
    return 1;
  } catch (const tl::DataError&) {
  }
  // Missing file -> IoError.
  try {
    tl::read_ppm_strict(scratch + "/missing.ppm");
    return 1;
  } catch (const tl::IoError&) {
  }
  return 0;
}

int test_timeline() {
  using tl::Rational;
  using tl::SpeedPiece;
  // Rational frame count: 1/2 s at 30000/1001 -> 15 frames.
  CHECK(tl::frames_for_duration(tl::parse_rational("1/2", "d"),
                                tl::parse_rational("30000/1001", "f")) == 15);
  // Uniform timeline endpoints.
  tl::Timeline uni(Rational{30, 1}, 15, {SpeedPiece{0, 15, Rational{1, 1}}});
  CHECK(uni.progress(0).num == 0);
  CHECK(uni.progress(14).num == uni.progress(14).den);
  // Piecewise speed: monotonic, still ends at 1.
  tl::Timeline var(Rational{30, 1}, 15,
                   {SpeedPiece{0, 8, Rational{1, 2}},
                    SpeedPiece{8, 15, Rational{3, 2}}});
  int64_t prev_num = -1;
  for (int64_t i = 0; i < 15; ++i) {
    const Rational p = var.progress(i);
    const __int128 lhs = static_cast<__int128>(p.num) * 1;
    CHECK(p.num >= 0 && p.num <= p.den);
    CHECK(static_cast<__int128>(p.num) * 1000000 / p.den >= prev_num);
    prev_num = static_cast<int64_t>(lhs * 1000000 / p.den);
  }
  CHECK(var.progress(14).num == var.progress(14).den);
  // Negative speed -> ArgError.
  try {
    tl::Timeline bad(Rational{30, 1}, 4, {SpeedPiece{0, 4, Rational{-1, 1}}});
    return 1;
  } catch (const tl::ArgError&) {
  }
  // Speed > 4x -> ArgError.
  try {
    tl::Timeline bad(Rational{30, 1}, 4, {SpeedPiece{0, 4, Rational{9, 2}}});
    return 1;
  } catch (const tl::ArgError&) {
  }
  // Gap between pieces -> ArgError.
  try {
    tl::Timeline bad(Rational{30, 1}, 10,
                     {SpeedPiece{0, 4, Rational{1, 1}},
                      SpeedPiece{6, 10, Rational{1, 1}}});
    return 1;
  } catch (const tl::ArgError&) {
  }
  // Overlap between pieces -> ArgError.
  try {
    tl::Timeline bad(Rational{30, 1}, 10,
                     {SpeedPiece{0, 6, Rational{1, 1}},
                      SpeedPiece{4, 10, Rational{1, 1}}});
    return 1;
  } catch (const tl::ArgError&) {
  }
  // Pieces not covering the whole range -> ArgError.
  try {
    tl::Timeline bad(Rational{30, 1}, 10, {SpeedPiece{0, 9, Rational{1, 1}}});
    return 1;
  } catch (const tl::ArgError&) {
  }
  // Invalid interval (empty) -> ArgError.
  try {
    tl::Timeline bad(Rational{30, 1}, 10, {SpeedPiece{0, 0, Rational{1, 1}}});
    return 1;
  } catch (const tl::ArgError&) {
  }
  // Zero denominator -> ArgError.
  try {
    tl::parse_rational("1/0", "fps");
    return 1;
  } catch (const tl::ArgError&) {
  }
  return 0;
}

int test_manifest(const std::string& scratch) {
  std::filesystem::create_directories(scratch);
  tl::RenderConfig cfg = demo_cfg();
  cfg.out_dir = scratch;
  cfg.transitions = {tl::TransitionType::Iris};
  const tl::TypeStats s =
      tl::render_sequence(cfg, tl::TransitionType::Iris, false);
  const std::string path = scratch + "/manifest.txt";
  tl::write_manifest(path, cfg, {s});
  const tl::Manifest m = tl::read_manifest(path);
  CHECK(m.stats.size() == 1);
  CHECK(m.stats[0].frame_hashes == s.frame_hashes);
  CHECK(m.cfg.width == 96 && m.cfg.height == 64);
  CHECK(m.cfg.fps.num == 30000 && m.cfg.fps.den == 1001);
  return 0;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 2) {
    std::fprintf(stderr, "usage: renderer_test <case> [scratch_dir]\n");
    return 2;
  }
  const std::string test_case = argv[1];
  const std::string scratch =
      argc > 2 ? argv[2] : (std::filesystem::temp_directory_path() /
                            "transition_lab_test")
                               .string();
  try {
    if (test_case == "endpoints") return test_endpoints();
    if (test_case == "determinism") return test_determinism();
    if (test_case == "concurrency") return test_concurrency();
    if (test_case == "ppm") return test_ppm(scratch);
    if (test_case == "timeline") return test_timeline();
    if (test_case == "manifest") return test_manifest(scratch);
    std::fprintf(stderr, "unknown test case '%s'\n", test_case.c_str());
    return 2;
  } catch (const std::exception& e) {
    std::fprintf(stderr, "unexpected exception: %s\n", e.what());
    return 1;
  }
}
