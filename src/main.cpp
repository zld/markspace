// subtitle-metrics: offline subtitle line-width / safe-area / wrap checker.
//
//   subtitle-metrics fixture --out FILE [--count N]
//   subtitle-metrics measure --input FILE --font PATH [--fallback-font PATH]...
//       --font-size PX --max-width PX --max-lines N [--jobs 1..32]
//       [--jsonl PATH|-] [--summary PATH]
//
// Exit codes: 0 ok, 2 parse/encoding errors, 3 font error, 4 write error,
// 64 usage error (including input over the 100 MB / 100k cue limits).

#include <atomic>
#include <cstdint>
#include <cstring>
#include <fstream>
#include <iostream>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

#include "subtitle/fixture.hpp"
#include "subtitle/layout.hpp"
#include "subtitle/parser.hpp"
#include "subtitle/summary_exporter.hpp"

namespace {

constexpr std::uint64_t kMaxInputBytes = 100ull * 1024 * 1024;
constexpr std::size_t kMaxCues = 100000;

int usage() {
  std::cerr <<
      "usage:\n"
      "  subtitle-metrics fixture --out FILE [--count N]\n"
      "  subtitle-metrics measure --input FILE --font PATH "
      "[--fallback-font PATH]...\n"
      "      --font-size PX --max-width PX --max-lines N [--jobs 1..32]\n"
      "      [--jsonl PATH|-] [--summary PATH]\n";
  return 64;
}

bool read_file(const std::string& path, std::string& out) {
  std::ifstream f(path, std::ios::binary | std::ios::ate);
  if (!f) return false;
  const std::streamoff size = f.tellg();
  if (size < 0 || static_cast<std::uint64_t>(size) > kMaxInputBytes) {
    std::cerr << "error: input exceeds 100 MB limit\n";
    return false;
  }
  out.resize(static_cast<std::size_t>(size));
  f.seekg(0);
  f.read(out.data(), size);
  return static_cast<bool>(f) || f.gcount() == size;
}

int cmd_fixture(int argc, char** argv) {
  std::string out_path;
  std::size_t count = 128;
  for (int i = 0; i < argc; ++i) {
    if (!std::strcmp(argv[i], "--out") && i + 1 < argc) {
      out_path = argv[++i];
    } else if (!std::strcmp(argv[i], "--count") && i + 1 < argc) {
      count = std::stoull(argv[++i]);
    } else {
      return usage();
    }
  }
  if (out_path.empty() || count == 0 || count > kMaxCues) return usage();
  const std::string srt = subtitle::generate_fixture(count);
  std::ofstream f(out_path, std::ios::binary | std::ios::trunc);
  f << srt;
  f.flush();
  if (!f) {
    std::cerr << "error: cannot write " << out_path << "\n";
    return 4;
  }
  std::cerr << "wrote " << count << " cues to " << out_path << "\n";
  return 0;
}

int cmd_measure(int argc, char** argv) {
  std::string input, jsonl_path = "-", summary_path;
  std::vector<std::string> fonts;
  double font_size = 0;
  long max_width = 0, max_lines = 0, jobs = 4;
  for (int i = 0; i < argc; ++i) {
    auto need = [&](const char* v) { return v; };
    if (!std::strcmp(argv[i], "--input") && i + 1 < argc)
      input = argv[++i];
    else if (!std::strcmp(argv[i], "--font") && i + 1 < argc)
      fonts.insert(fonts.begin(), argv[++i]);  // primary stays first
    else if (!std::strcmp(argv[i], "--fallback-font") && i + 1 < argc)
      fonts.push_back(argv[++i]);
    else if (!std::strcmp(argv[i], "--font-size") && i + 1 < argc)
      font_size = std::stod(need(argv[++i]));
    else if (!std::strcmp(argv[i], "--max-width") && i + 1 < argc)
      max_width = std::stol(argv[++i]);
    else if (!std::strcmp(argv[i], "--max-lines") && i + 1 < argc)
      max_lines = std::stol(argv[++i]);
    else if (!std::strcmp(argv[i], "--jobs") && i + 1 < argc)
      jobs = std::stol(argv[++i]);
    else if (!std::strcmp(argv[i], "--jsonl") && i + 1 < argc)
      jsonl_path = argv[++i];
    else if (!std::strcmp(argv[i], "--summary") && i + 1 < argc)
      summary_path = argv[++i];
    else
      return usage();
  }
  if (input.empty() || fonts.empty() || font_size <= 0 || max_width <= 0 ||
      max_lines <= 0 || jobs < 1 || jobs > 32)
    return usage();

  std::string bytes;
  if (!read_file(input, bytes)) {
    std::cerr << "error: cannot read " << input << "\n";
    return 64;
  }
  subtitle::ParseResult parsed = subtitle::parse_srt(bytes);
  if (parsed.cues.size() > kMaxCues) {
    std::cerr << "error: cue count exceeds 100000 limit\n";
    return 64;
  }
  for (const auto& e : parsed.errors)
    std::cerr << "parse error line " << e.line << ": " << e.message << "\n";

  // Validate fonts up front; each worker then gets its own engine/FT faces.
  try {
    subtitle::LayoutEngine probe(fonts, font_size);
  } catch (const subtitle::FontError& e) {
    std::cerr << "font error: " << e.what() << "\n";
    return 3;
  }

  const subtitle::LayoutParams params{max_width * 64,
                                      static_cast<int>(max_lines)};
  const std::size_t n = parsed.cues.size();
  std::vector<subtitle::CueLayout> layouts(n);
  std::atomic<std::size_t> next{0};
  std::atomic<bool> font_failed{false};
  std::string font_error_msg;
  std::mutex msg_mu;

  auto worker = [&] {
    try {
      subtitle::LayoutEngine engine(fonts, font_size);
      for (std::size_t i = next.fetch_add(1); i < n; i = next.fetch_add(1))
        layouts[i] = engine.layout(parsed.cues[i], params);
    } catch (const subtitle::FontError& e) {
      std::lock_guard<std::mutex> lk(msg_mu);
      font_error_msg = e.what();
      font_failed = true;
    }
  };
  std::vector<std::thread> pool;
  for (long t = 0; t < jobs; ++t) pool.emplace_back(worker);
  for (auto& t : pool) t.join();
  if (font_failed) {  // stop before touching disk
    std::cerr << "font error: " << font_error_msg << "\n";
    return 3;
  }

  subtitle::Summary summary;
  summary.cue_count = n;
  summary.parse_errors = parsed.errors.size();
  summary.encoding_errors = parsed.replaced_chars;
  for (const auto& l : layouts) {
    if (l.overflow) ++summary.overflow_entries;
    summary.missing_glyphs += l.missing_glyphs;
    if (l.requires_shaping) ++summary.shaping_cues;
  }

  if (!subtitle::write_jsonl(jsonl_path, parsed.cues, layouts)) {
    std::cerr << "error: cannot write jsonl to " << jsonl_path << "\n";
    return 4;
  }
  if (!summary_path.empty() &&
      !subtitle::write_summary_atomic(summary_path, summary)) {
    std::cerr << "error: cannot write summary to " << summary_path << "\n";
    return 4;
  }
  std::cerr << "summary: " << subtitle::summary_to_json(summary) << "\n";
  return (summary.parse_errors > 0 || summary.encoding_errors > 0) ? 2 : 0;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 2) return usage();
  try {
    if (!std::strcmp(argv[1], "fixture")) return cmd_fixture(argc - 2, argv + 2);
    if (!std::strcmp(argv[1], "measure")) return cmd_measure(argc - 2, argv + 2);
    return usage();
  } catch (const std::exception& e) {
    std::cerr << "error: " << e.what() << "\n";
    return 64;
  }
}
