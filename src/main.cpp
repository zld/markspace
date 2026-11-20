#include <cstdio>
#include <cstring>
#include <string>

#include "fmp4/box.hpp"
#include "fmp4/fixture.hpp"
#include "fmp4/repacker.hpp"

using namespace fmp4;

namespace {

int usage() {
  std::fprintf(stderr,
               "usage:\n"
               "  fmp4ctl fixture --out DIR\n"
               "  fmp4ctl repack --init FILE --segments DIR --out DIR\n"
               "                 --first-seq N --last-seq N\n"
               "                 --max-decode-delta-ticks T\n"
               "                 [--json] [--report FILE]\n");
  return kExitUsage;
}

bool parse_u32(const std::string& s, uint32_t& out) {
  try {
    out = std::stoul(s);
    return true;
  } catch (...) {
    return false;
  }
}

bool parse_i64(const std::string& s, int64_t& out) {
  try {
    out = std::stoll(s);
    return true;
  } catch (...) {
    return false;
  }
}

int run_fixture(int argc, char** argv) {
  std::string out;
  for (int i = 2; i < argc; ++i) {
    if (!std::strcmp(argv[i], "--out") && i + 1 < argc) {
      out = argv[++i];
    } else {
      return usage();
    }
  }
  if (out.empty()) return usage();
  write_fixture(out);
  std::printf("FIXTURE_OK=%s\n", out.c_str());
  return kExitOk;
}

int run_repack_cmd(int argc, char** argv) {
  RepackOptions opt;
  bool have_init = false, have_seg = false, have_out = false;
  bool have_first = false, have_last = false, have_tol = false;
  for (int i = 2; i < argc; ++i) {
    std::string a = argv[i];
    std::string v;
    auto next = [&]() -> const char* { return i + 1 < argc ? argv[++i] : nullptr; };
    if (a == "--init") {
      if (const char* p = next()) { opt.init_path = p; have_init = true; }
    } else if (a == "--segments") {
      if (const char* p = next()) { opt.segments_dir = p; have_seg = true; }
    } else if (a == "--out") {
      if (const char* p = next()) { opt.out_dir = p; have_out = true; }
    } else if (a == "--first-seq") {
      if (const char* p = next()) have_first = parse_u32(p, opt.first_seq);
    } else if (a == "--last-seq") {
      if (const char* p = next()) have_last = parse_u32(p, opt.last_seq);
    } else if (a == "--max-decode-delta-ticks") {
      if (const char* p = next())
        have_tol = parse_i64(p, opt.max_decode_delta_ticks);
    } else if (a == "--json") {
      opt.json_stdout = true;
    } else if (a == "--report") {
      if (const char* p = next()) opt.report_path = p;
      else return usage();
    } else {
      return usage();
    }
  }
  if (!(have_init && have_seg && have_out && have_first && have_last &&
        have_tol))
    return usage();
  return run_repack(opt);
}

}  // namespace

int main(int argc, char** argv) {
  try {
    if (argc < 2) return usage();
    std::string cmd = argv[1];
    if (cmd == "fixture") return run_fixture(argc, argv);
    if (cmd == "repack") return run_repack_cmd(argc, argv);
    return usage();
  } catch (const Fmp4Error& e) {
    std::fprintf(stderr, "ERROR: %s\n", e.what());
    return e.code;
  } catch (const std::exception& e) {
    std::fprintf(stderr, "ERROR: %s\n", e.what());
    return kExitUsage;
  }
}
