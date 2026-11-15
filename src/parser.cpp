// SRT parser. Tolerant: bad cue blocks are skipped with a recorded error,
// parsing resumes at the next blank-line boundary.

#include "subtitle/parser.hpp"

#include <cctype>

#include "subtitle/utf8.hpp"

namespace subtitle {
namespace {

bool is_index_line(const std::string& s) {
  if (s.empty()) return false;
  for (char c : s)
    if (!std::isdigit(static_cast<unsigned char>(c))) return false;
  return true;
}

// Parse "HH:MM:SS,mmm" (',' or '.'), strict ranges. Returns ms or -1.
std::int64_t parse_timestamp(const std::string& s, std::size_t& consumed) {
  auto number = [&](std::size_t pos, int digits, long& out) -> bool {
    if (pos + digits > s.size()) return false;
    long v = 0;
    for (int k = 0; k < digits; ++k) {
      char c = s[pos + k];
      if (!std::isdigit(static_cast<unsigned char>(c))) return false;
      v = v * 10 + (c - '0');
    }
    out = v;
    return true;
  };
  long h, m, sec, ms;
  std::size_t p = 0;
  if (!number(p, 2, h)) return -1;
  p += 2;
  if (p >= s.size() || s[p] != ':') return -1;
  if (!number(++p, 2, m)) return -1;
  p += 2;
  if (p >= s.size() || s[p] != ':') return -1;
  if (!number(++p, 2, sec)) return -1;
  p += 2;
  if (p >= s.size() || (s[p] != ',' && s[p] != '.')) return -1;
  if (!number(++p, 3, ms)) return -1;
  p += 3;
  if (m > 59 || sec > 59) return -1;
  consumed = p;
  return ((h * 60 + m) * 60 + sec) * 1000 + ms;
}

std::string trim(const std::string& s) {
  std::size_t b = s.find_first_not_of(" \t");
  if (b == std::string::npos) return "";
  return s.substr(b, s.find_last_not_of(" \t") - b + 1);
}

}  // namespace

ParseResult parse_srt(const std::string& bytes) {
  ParseResult result;

  std::string text = bytes;
  if (text.size() >= 3 && text.compare(0, 3, "\xEF\xBB\xBF") == 0)
    text.erase(0, 3);  // UTF-8 BOM
  text = utf8::sanitize(text, &result.replaced_chars);

  // Normalize line endings, then split keeping 1-based line numbers.
  std::string norm;
  norm.reserve(text.size());
  for (std::size_t i = 0; i < text.size(); ++i) {
    if (text[i] == '\r') {
      if (i + 1 < text.size() && text[i + 1] == '\n') continue;  // CRLF
      norm.push_back('\n');                                      // lone CR
    } else {
      norm.push_back(text[i]);
    }
  }
  std::vector<std::string> lines;
  std::size_t start = 0;
  for (std::size_t i = 0; i <= norm.size(); ++i) {
    if (i == norm.size() || norm[i] == '\n') {
      lines.push_back(norm.substr(start, i - start));
      start = i + 1;
    }
  }

  std::size_t i = 0;
  auto skip_blank = [&] {
    while (i < lines.size() && trim(lines[i]).empty()) ++i;
  };
  auto skip_block = [&] {  // skip to just past the next blank line
    while (i < lines.size() && !trim(lines[i]).empty()) ++i;
  };

  while (true) {
    skip_blank();
    if (i >= lines.size()) break;

    const std::size_t cue_line = i + 1;
    if (!is_index_line(trim(lines[i]))) {
      result.errors.push_back({cue_line, "expected cue index"});
      skip_block();
      continue;
    }
    ++i;
    if (i >= lines.size() || trim(lines[i]).empty()) {
      result.errors.push_back({cue_line, "missing timestamp line"});
      continue;
    }

    const std::size_t ts_line = i + 1;
    const std::string ts = trim(lines[i]);
    const std::size_t arrow = ts.find("-->");
    std::int64_t t0 = -1, t1 = -1;
    if (arrow != std::string::npos) {
      std::size_t used = 0;
      std::string left = trim(ts.substr(0, arrow));
      std::string right = trim(ts.substr(arrow + 3));
      t0 = parse_timestamp(left, used);
      if (used != left.size()) t0 = -1;
      t1 = parse_timestamp(right, used);
      if (used != right.size()) t1 = -1;
    }
    if (t0 < 0 || t1 < 0) {
      result.errors.push_back({ts_line, "malformed timestamp"});
      skip_block();
      continue;
    }
    if (t1 <= t0) {
      result.errors.push_back({ts_line, "end not after start"});
      skip_block();
      continue;
    }
    ++i;

    Cue cue;
    cue.index = result.cues.size();
    cue.start_ms = t0;
    cue.end_ms = t1;
    cue.source_line = cue_line;
    bool first = true;
    while (i < lines.size() && !trim(lines[i]).empty()) {
      if (!first) cue.text.push_back('\n');
      cue.text += lines[i];  // keep intra-line spacing verbatim
      first = false;
      ++i;
    }
    result.cues.push_back(std::move(cue));
  }
  return result;
}

}  // namespace subtitle
