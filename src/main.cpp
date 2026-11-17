#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

#include "errors.hpp"
#include "ppm.hpp"
#include "render_pipeline.hpp"

namespace {

const char* kUsage =
    "usage:\n"
    "  transition_lab render --demo --fps N[/D] --duration N[/D] --size WxH\n"
    "      --transitions fade[,wipe,iris] --jobs K --out DIR [--seed N]\n"
    "      [--speeds begin:end:speed[,...]]\n"
    "  transition_lab verify --dir DIR --repeat K [--jobs K]\n"
    "exit codes: 0 ok, 2 bad arguments, 3 validation failed, 4 file IO error\n";

[[noreturn]] void usage_error(const std::string& msg) {
  std::fprintf(stderr, "error: %s\n%s", msg.c_str(), kUsage);
  throw tl::ArgError(msg);
}

std::string require_value(int* i, int argc, char** argv, const char* flag) {
  if (*i + 1 >= argc) usage_error(std::string("missing value for ") + flag);
  ++*i;
  return argv[*i];
}

void parse_size(const std::string& s, int* w, int* h) {
  const size_t x = s.find('x');
  if (x == std::string::npos || x == 0 || x + 1 >= s.size())
    usage_error("bad --size '" + s + "', expected WxH");
  try {
    *w = std::stoi(s.substr(0, x));
    *h = std::stoi(s.substr(x + 1));
  } catch (...) {
    usage_error("bad --size '" + s + "', expected WxH");
  }
}

int parse_int(const std::string& s, const char* flag) {
  size_t used = 0;
  int v = 0;
  try {
    v = std::stoi(s, &used);
  } catch (...) {
    usage_error(std::string("bad integer for ") + flag + ": '" + s + "'");
  }
  if (used != s.size())
    usage_error(std::string("bad integer for ") + flag + ": '" + s + "'");
  return v;
}

std::vector<tl::TransitionType> parse_transitions(const std::string& s) {
  std::vector<tl::TransitionType> out;
  size_t pos = 0;
  while (pos <= s.size()) {
    const size_t comma = s.find(',', pos);
    const std::string name =
        s.substr(pos, comma == std::string::npos ? comma : comma - pos);
    tl::TransitionType t;
    if (!tl::parse_transition(name, t))
      usage_error("unknown transition '" + name + "'");
    for (tl::TransitionType prev : out)
      if (prev == t) usage_error("duplicate transition '" + name + "'");
    out.push_back(t);
    if (comma == std::string::npos) break;
    pos = comma + 1;
  }
  if (out.empty()) usage_error("empty --transitions list");
  return out;
}

std::vector<tl::SpeedPiece> parse_speeds(const std::string& s) {
  std::vector<tl::SpeedPiece> out;
  size_t pos = 0;
  while (pos < s.size()) {
    const size_t comma = s.find(',', pos);
    const std::string tok =
        s.substr(pos, comma == std::string::npos ? comma : comma - pos);
    const size_t c1 = tok.find(':');
    const size_t c2 = tok.find(':', c1 == std::string::npos ? c1 : c1 + 1);
    if (c1 == std::string::npos || c2 == std::string::npos)
      usage_error("bad --speeds piece '" + tok + "', expected begin:end:speed");
    tl::SpeedPiece p;
    p.begin_frame = parse_int(tok.substr(0, c1), "--speeds");
    p.end_frame = parse_int(tok.substr(c1 + 1, c2 - c1 - 1), "--speeds");
    p.speed = tl::parse_rational(tok.substr(c2 + 1), "speed");
    out.push_back(p);
    if (comma == std::string::npos) break;
    pos = comma + 1;
  }
  return out;
}

int cmd_render(int argc, char** argv) {
  tl::RenderConfig cfg;
  bool have_fps = false, have_duration = false, have_size = false;
  bool have_transitions = false, have_out = false;
  for (int i = 0; i < argc; ++i) {
    const std::string a = argv[i];
    if (a == "--demo") {
      // Demo mode uses the built-in procedural biome pair; nothing to do.
    } else if (a == "--fps") {
      cfg.fps = tl::parse_rational(require_value(&i, argc, argv, "--fps"), "fps");
      have_fps = true;
    } else if (a == "--duration") {
      cfg.duration =
          tl::parse_rational(require_value(&i, argc, argv, "--duration"),
                             "duration");
      have_duration = true;
    } else if (a == "--size") {
      parse_size(require_value(&i, argc, argv, "--size"), &cfg.width,
                 &cfg.height);
      have_size = true;
    } else if (a == "--transitions") {
      cfg.transitions =
          parse_transitions(require_value(&i, argc, argv, "--transitions"));
      have_transitions = true;
    } else if (a == "--jobs") {
      cfg.jobs = parse_int(require_value(&i, argc, argv, "--jobs"), "--jobs");
    } else if (a == "--seed") {
      cfg.seed = static_cast<uint32_t>(
          parse_int(require_value(&i, argc, argv, "--seed"), "--seed"));
    } else if (a == "--speeds") {
      cfg.speed_pieces =
          parse_speeds(require_value(&i, argc, argv, "--speeds"));
    } else if (a == "--out") {
      cfg.out_dir = require_value(&i, argc, argv, "--out");
      have_out = true;
    } else {
      usage_error("unknown render argument '" + a + "'");
    }
  }
  if (!have_fps || !have_duration || !have_size || !have_transitions ||
      !have_out)
    usage_error("render requires --fps, --duration, --size, --transitions, "
                "--out");
  if (cfg.jobs < 1) usage_error("--jobs must be >= 1");

  std::vector<tl::TypeStats> stats;
  for (tl::TransitionType t : cfg.transitions) {
    const tl::TypeStats s = tl::render_sequence(cfg, t, /*write_files=*/true);
    std::printf("%s\n", tl::format_stats_line(s).c_str());
    stats.push_back(s);
  }
  tl::write_manifest(cfg.out_dir + "/manifest.txt", cfg, stats);
  std::printf("render ok: %s\n", cfg.out_dir.c_str());
  return 0;
}

int cmd_verify(int argc, char** argv) {
  std::string dir;
  int repeat = 1;
  int jobs = 4;
  for (int i = 0; i < argc; ++i) {
    const std::string a = argv[i];
    if (a == "--dir") {
      dir = require_value(&i, argc, argv, "--dir");
    } else if (a == "--repeat") {
      repeat = parse_int(require_value(&i, argc, argv, "--repeat"), "--repeat");
    } else if (a == "--jobs") {
      jobs = parse_int(require_value(&i, argc, argv, "--jobs"), "--jobs");
    } else {
      usage_error("unknown verify argument '" + a + "'");
    }
  }
  if (dir.empty()) usage_error("verify requires --dir");
  if (repeat < 1) usage_error("--repeat must be >= 1");
  if (jobs < 1) usage_error("--jobs must be >= 1");

  tl::Manifest m = tl::read_manifest(dir + "/manifest.txt");

  // 1) Strict-read every recorded frame file and check its hash.
  for (const tl::TypeStats& s : m.stats) {
    for (int64_t i = 0; i < s.frames; ++i) {
      const std::string path = dir + "/" + tl::frame_filename(s.type, i);
      const tl::FrameBuffer fb = tl::read_ppm_strict(path);
      const uint64_t h = tl::hash_frame(fb);
      if (h != s.frame_hashes[static_cast<size_t>(i)])
        throw tl::DataError("verify: hash mismatch in '" + path + "': got " +
                            tl::hash_hex(h) + ", manifest says " +
                            tl::hash_hex(s.frame_hashes[static_cast<size_t>(i)]));
    }
  }

  // 2) Re-render `repeat` times; hashes and stats must match the manifest.
  m.cfg.jobs = jobs;
  for (int r = 0; r < repeat; ++r) {
    for (const tl::TypeStats& expected : m.stats) {
      const tl::TypeStats got =
          tl::render_sequence(m.cfg, expected.type, /*write_files=*/false);
      if (got.frame_hashes != expected.frame_hashes ||
          got.first_hash != expected.first_hash ||
          got.last_hash != expected.last_hash ||
          got.mid_changed_pixels != expected.mid_changed_pixels ||
          got.mid_mean_abs_delta_milli != expected.mid_mean_abs_delta_milli)
        throw tl::DataError("verify: re-render mismatch for type " +
                            std::string(tl::transition_name(expected.type)) +
                            " on pass " + std::to_string(r + 1));
    }
  }

  for (const tl::TypeStats& s : m.stats)
    std::printf("%s\n", tl::format_stats_line(s).c_str());
  std::printf("verify ok: %s (%d pass%s)\n", dir.c_str(), repeat,
              repeat == 1 ? "" : "es");
  return 0;
}

}  // namespace

int main(int argc, char** argv) {
  try {
    if (argc < 2) usage_error("missing subcommand");
    const std::string cmd = argv[1];
    if (cmd == "render") return cmd_render(argc - 2, argv + 2);
    if (cmd == "verify") return cmd_verify(argc - 2, argv + 2);
    usage_error("unknown subcommand '" + cmd + "'");
  } catch (const tl::ArgError& e) {
    std::fprintf(stderr, "argument error: %s\n", e.what());
    return 2;
  } catch (const tl::DataError& e) {
    std::fprintf(stderr, "validation error: %s\n", e.what());
    return 3;
  } catch (const tl::IoError& e) {
    std::fprintf(stderr, "io error: %s\n", e.what());
    return 4;
  } catch (const std::exception& e) {
    std::fprintf(stderr, "error: %s\n", e.what());
    return 3;
  }
}
