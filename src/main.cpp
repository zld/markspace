#include <cstdio>
#include <fstream>
#include <initializer_list>
#include <map>
#include <sstream>
#include <string>
#include <vector>

#include "demo_factory.hpp"
#include "manifest.hpp"
#include "ppm_writer.hpp"
#include "renderer.hpp"
#include "timeline.hpp"

namespace {

// Exit codes: 0 ok, 2 args/syntax, 3 timeline invalid, 4 I/O, 5 consistency.

int usage() {
  std::fprintf(stderr,
               "usage: transition_lab make-demo --out PATH\n"
               "       transition_lab render --manifest PATH --out DIR "
               "[--verify]\n");
  return 2;
}

// Parses "--key value" pairs; returns false on unknown/missing args.
bool parse_opts(int argc, char** argv, int start,
                std::initializer_list<const char*> keys,
                std::map<std::string, std::string>& opts,
                std::vector<std::string>& flags) {
  for (int i = start; i < argc; ++i) {
    std::string a = argv[i];
    if (a.rfind("--", 0) != 0) return false;
    bool is_key = false;
    for (const char* k : keys)
      if (a == std::string("--") + k) is_key = true;
    if (is_key) {
      if (i + 1 >= argc) return false;
      opts[a.substr(2)] = argv[++i];
    } else {
      flags.push_back(a);
    }
  }
  return true;
}

bool has_flag(const std::vector<std::string>& flags, const std::string& f) {
  for (const auto& x : flags)
    if (x == f) return true;
  return false;
}

int cmd_make_demo(const std::map<std::string, std::string>& opts,
                  const std::vector<std::string>& flags) {
  auto it = opts.find("out");
  if (it == opts.end() || !flags.empty()) return usage();
  if (!tl::path_is_safe(it->second)) {
    std::fprintf(stderr, "error: unsafe output path %s\n", it->second.c_str());
    return 2;
  }
  std::string err;
  if (!tl::write_demo(it->second, err)) {
    std::fprintf(stderr, "error: %s\n", err.c_str());
    return 4;
  }
  std::printf("demo manifest written to %s\n", it->second.c_str());
  return 0;
}

int cmd_render(const std::map<std::string, std::string>& opts,
               const std::vector<std::string>& flags) {
  auto mi = opts.find("manifest");
  auto oi = opts.find("out");
  if (mi == opts.end() || oi == opts.end()) return usage();
  for (const auto& f : flags)
    if (f != "--verify") return usage();
  if (!tl::path_is_safe(oi->second)) {
    std::fprintf(stderr, "error: unsafe output path %s\n", oi->second.c_str());
    return 2;
  }

  std::ifstream in(mi->second);
  if (!in) {
    std::fprintf(stderr, "error: cannot read manifest %s\n", mi->second.c_str());
    return 4;
  }
  std::ostringstream ss;
  ss << in.rdbuf();

  tl::Manifest m;
  std::string err;
  if (!tl::parse_manifest(ss.str(), m, err)) {
    std::fprintf(stderr, "manifest error: %s\n", err.c_str());
    return 2;
  }

  tl::RenderOptions ro;
  ro.out_dir = oi->second;
  ro.verify = has_flag(flags, "--verify");
  try {
    return tl::run_render(m, ro);
  } catch (const tl::TimelineError& e) {
    std::fprintf(stderr, "timeline error: %s\n", e.what());
    return 3;
  }
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 2) return usage();
  std::string cmd = argv[1];
  std::map<std::string, std::string> opts;
  std::vector<std::string> flags;
  if (cmd == "make-demo") {
    if (!parse_opts(argc, argv, 2, {"out"}, opts, flags)) return usage();
    return cmd_make_demo(opts, flags);
  }
  if (cmd == "render") {
    if (!parse_opts(argc, argv, 2, {"manifest", "out"}, opts, flags))
      return usage();
    return cmd_render(opts, flags);
  }
  return usage();
}
