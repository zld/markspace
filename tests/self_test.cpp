// Self tests: UTF-8, clusters, SRT parsing, line breaking, layout,
// exporter and fixture invariants. Font-dependent tests use the first
// available system font and are skipped (with a note) when none exists.

#include <cstdio>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>

#include <nlohmann/json.hpp>

#include "subtitle/fixture.hpp"
#include "subtitle/layout.hpp"
#include "subtitle/line_breaker.hpp"
#include "subtitle/parser.hpp"
#include "subtitle/summary_exporter.hpp"
#include "subtitle/utf8.hpp"

namespace {

int failures = 0;
int checks = 0;

#define CHECK(cond)                                              \
  do {                                                           \
    ++checks;                                                    \
    if (!(cond)) {                                               \
      ++failures;                                                \
      std::cerr << "FAIL " << __FILE__ << ":" << __LINE__ << " " \
                << #cond << "\n";                                \
    }                                                            \
  } while (0)

std::string find_font(const std::vector<std::string>& candidates) {
  for (const auto& p : candidates) {
    std::ifstream f(p, std::ios::binary);
    if (f.good()) return p;
  }
  return "";
}

void test_utf8() {
  auto d = subtitle::utf8::decode("a\xC3\xA9" "b");  // a é b
  CHECK(d.size() == 3 && d[1].cp == 0xE9 && d[1].valid);
  std::uint64_t replaced = 0;
  std::string s = subtitle::utf8::sanitize("ok\xFF\xFE" "end", &replaced);
  CHECK(replaced == 2);
  CHECK(s.find("\xEF\xBF\xBD") != std::string::npos);
  // Truncated multibyte sequence counts once and decoding continues.
  replaced = 0;
  subtitle::utf8::sanitize("x\xE4\xBD", &replaced);
  CHECK(replaced == 2);
}

void test_clusters() {
  // "á" as a + U+0301 must stay one cluster; newline is its own cluster.
  auto cs = subtitle::segment_clusters("a\xCC\x81" "b\n");
  CHECK(cs.size() == 3);
  CHECK(cs[0].byte_len == 3);  // 'a' + combining mark
  CHECK(cs[2].kind == subtitle::Cluster::Newline);
}

void test_parser() {
  // BOM + CRLF + one bad timestamp (line 9) + multiline text.
  std::string srt =
      "\xEF\xBB\xBF"
      "1\r\n00:00:01,000 --> 00:00:02,000\r\nfirst\r\n\r\n"
      "2\r\n00:00:03,000 --> 00:00:04,000\r\nline one\r\nline two\r\n\r\n"
      "3\r\nnot-a-timestamp\r\nbad cue\r\n\r\n"
      "4\r\n00:00:05,000 --> 00:00:06,500\r\nlast\r\n";
  auto r = subtitle::parse_srt(srt);
  CHECK(r.cues.size() == 3);
  CHECK(r.errors.size() == 1);
  CHECK(r.errors[0].line == 11);
  CHECK(r.cues[1].text == "line one\nline two");
  CHECK(r.cues[2].start_ms == 5000 && r.cues[2].end_ms == 6500);
  // end <= start is rejected too.
  auto r2 = subtitle::parse_srt("1\n00:00:02,000 --> 00:00:01,000\nx\n");
  CHECK(r2.cues.empty() && r2.errors.size() == 1);
}

void test_breaker() {
  using subtitle::Cluster;
  auto widths_of = [](const std::vector<Cluster>& cs) {
    return [&cs](std::size_t a, std::size_t b) -> subtitle::Fixed {
      subtitle::Fixed w = 0;
      for (std::size_t i = a; i < b; ++i)
        w += cs[i].kind == Cluster::Space ? 5 : 10;
      return w;
    };
  };
  // Unbreakable long word: single line, flagged not-fitting.
  {
    auto cs = subtitle::segment_clusters("ABCDEFGHIJ");
    auto br = subtitle::break_lines(cs, widths_of(cs), 50);
    CHECK(br.lines.size() == 1 && !br.fits[0]);
  }
  // CJK closing punctuation must not start a line: 你好，世界 at width 35
  // breaks as [你好，][世界], never before '，'.
  {
    auto cs = subtitle::segment_clusters("你好，世界");
    auto br = subtitle::break_lines(cs, widths_of(cs), 35);
    CHECK(br.lines.size() == 2);
    CHECK(br.lines[0].second == 3);  // includes '，'
  }
  // Explicit newline forces a break.
  {
    auto cs = subtitle::segment_clusters("ab\ncd");
    auto br = subtitle::break_lines(cs, widths_of(cs), 1000);
    CHECK(br.lines.size() == 2);
  }
  // Spaces are consumed at break points.
  {
    auto cs = subtitle::segment_clusters("aa bb cc");
    auto br = subtitle::break_lines(cs, widths_of(cs), 25);
    CHECK(br.lines.size() == 3);
    for (auto& l : br.lines)
      CHECK(cs[l.first].kind != Cluster::Space);
  }
}

void test_layout(const std::string& font) {
  subtitle::LayoutEngine engine({font}, 32.0);
  subtitle::Cue cue;
  cue.index = 0;
  cue.text = "Hello world, this wraps somewhere.";
  subtitle::LayoutParams narrow{300 * 64, 4};
  subtitle::LayoutParams wide{4000 * 64, 4};
  auto a = engine.layout(cue, narrow);
  auto b = engine.layout(cue, wide);
  CHECK(a.lines.size() > b.lines.size());
  CHECK(b.bbox.width() > 0 && b.bbox.height() > 0);
  CHECK(a.wrap_offsets.size() + 1 == a.lines.size());
  // Offsets are byte offsets into the cue text, strictly increasing.
  for (std::size_t i = 1; i < a.wrap_offsets.size(); ++i)
    CHECK(a.wrap_offsets[i] > a.wrap_offsets[i - 1]);
  // Missing glyph: noncharacter U+10FFFE is in no real font.
  subtitle::Cue miss;
  miss.text = "x\xF4\x8F\xBF\xBE";
  auto m = engine.layout(miss, wide);
  CHECK(m.missing_glyphs == 1);
  // Line-count overflow.
  subtitle::LayoutParams one_line{80 * 64, 1};
  auto o = engine.layout(cue, one_line);
  CHECK(o.overflow && o.line_overflow);
}

void test_exporter() {
  subtitle::Cue cue;
  cue.index = 3;
  cue.start_ms = 100;
  cue.end_ms = 200;
  subtitle::CueLayout lay;
  lay.cue_index = 3;
  lay.bbox = subtitle::BBox{1, 2, 65, 66};
  lay.overflow = true;
  auto rec = nlohmann::json::parse(subtitle::cue_to_jsonl(cue, lay));
  CHECK(rec["index"] == 3);
  CHECK(rec["bbox_26_6"]["w"] == 64);
  CHECK(rec["overflow"] == true);
  subtitle::Summary s;
  s.cue_count = 7;
  CHECK(subtitle::write_summary_atomic("/tmp/subtitle-selftest-summary.json", s));
  std::ifstream f("/tmp/subtitle-selftest-summary.json");
  auto j = nlohmann::json::parse(f);
  CHECK(j["cue_count"] == 7);
}

void test_fixture() {
  const std::string srt = subtitle::generate_fixture(128);
  auto r = subtitle::parse_srt(srt);
  CHECK(r.errors.empty());
  CHECK(r.cues.size() == 128);
  std::size_t long_tokens = 0;
  for (const auto& c : r.cues)
    if (c.text.size() >= 60 && c.text.find(' ') == std::string::npos &&
        c.text.find('\n') == std::string::npos)
      ++long_tokens;
  CHECK(long_tokens == 8);
}

}  // namespace

int main() {
  test_utf8();
  test_clusters();
  test_parser();
  test_breaker();
  test_exporter();
  test_fixture();

  const std::string font = find_font({
      "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
      "/System/Library/Fonts/Supplemental/Arial.ttf",
      "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
  });
  if (font.empty()) {
    std::cerr << "note: no system font found, layout tests skipped\n";
  } else {
    try {
      test_layout(font);
    } catch (const std::exception& e) {
      ++failures;
      std::cerr << "FAIL layout tests threw: " << e.what() << "\n";
    }
  }

  std::cerr << checks << " checks, " << failures << " failures\n";
  return failures == 0 ? 0 : 1;
}
