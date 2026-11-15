#pragma once

// UTF-8 decoding, sanitizing and Unicode classification helpers shared by
// the parser (bad-encoding replacement) and the line breaker (clusters).

#include <cstdint>
#include <string>
#include <vector>

namespace subtitle::utf8 {

struct CodePoint {
  std::uint32_t cp = 0;  // decoded value; U+FFFD when invalid
  std::size_t offset = 0;
  std::size_t len = 0;  // bytes consumed, always >= 1
  bool valid = true;
};

// Decode the whole string; invalid sequences become U+FFFD (valid=false),
// consuming one byte each so decoding always makes progress.
inline std::vector<CodePoint> decode(const std::string& s) {
  std::vector<CodePoint> out;
  out.reserve(s.size());
  std::size_t i = 0;
  const std::size_t n = s.size();
  while (i < n) {
    const unsigned char c = static_cast<unsigned char>(s[i]);
    if (c < 0x80) {
      out.push_back({c, i, 1, true});
      ++i;
      continue;
    }
    int extra = 0;
    std::uint32_t cp = 0, minv = 0;
    if ((c & 0xE0) == 0xC0) { extra = 1; cp = c & 0x1F; minv = 0x80; }
    else if ((c & 0xF0) == 0xE0) { extra = 2; cp = c & 0x0F; minv = 0x800; }
    else if ((c & 0xF8) == 0xF0) { extra = 3; cp = c & 0x07; minv = 0x10000; }
    else { out.push_back({0xFFFD, i, 1, false}); ++i; continue; }
    bool ok = (n - i - 1) >= static_cast<std::size_t>(extra);
    for (int k = 1; ok && k <= extra; ++k) {
      const unsigned char t = static_cast<unsigned char>(s[i + k]);
      if ((t & 0xC0) != 0x80) { ok = false; break; }
      cp = (cp << 6) | (t & 0x3F);
    }
    if (ok && (cp < minv || cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)))
      ok = false;
    if (!ok) {
      out.push_back({0xFFFD, i, 1, false});
      ++i;
      continue;
    }
    out.push_back({cp, i, static_cast<std::size_t>(extra) + 1, true});
    i += extra + 1;
  }
  return out;
}

// Rebuild the string with invalid sequences replaced by U+FFFD.
// `replaced` (optional) receives the number of replacements.
inline std::string sanitize(const std::string& s, std::uint64_t* replaced) {
  std::string out;
  out.reserve(s.size());
  std::uint64_t count = 0;
  for (const CodePoint& d : decode(s)) {
    if (d.valid) {
      out.append(s, d.offset, d.len);
    } else {
      out.append("\xEF\xBF\xBD", 3);
      ++count;
    }
  }
  if (replaced) *replaced = count;
  return out;
}

inline bool in(std::uint32_t cp, std::uint32_t lo, std::uint32_t hi) {
  return cp >= lo && cp <= hi;
}

// Combining marks, variation selectors and joiners: extend the base cluster.
inline bool is_cluster_extender(std::uint32_t cp) {
  return in(cp, 0x0300, 0x036F) || in(cp, 0x1AB0, 0x1AFF) ||
         in(cp, 0x1DC0, 0x1DFF) || in(cp, 0x20D0, 0x20FF) ||
         in(cp, 0xFE00, 0xFE0F) || in(cp, 0xFE20, 0xFE2F) ||
         in(cp, 0xE0100, 0xE01EF) || cp == 0x200D || cp == 0x3099 ||
         cp == 0x309A;
}

// CJK ideographs, kana, hangul syllables and CJK punctuation block.
inline bool is_cjk(std::uint32_t cp) {
  return in(cp, 0x2E80, 0x9FFF) || in(cp, 0xAC00, 0xD7AF) ||
         in(cp, 0xF900, 0xFAFF) || in(cp, 0x20000, 0x2FA1F);
}

// Punctuation that must not start a line (closing / following marks).
inline bool is_no_line_start(std::uint32_t cp) {
  switch (cp) {
    case 0x3001: case 0x3002: case 0xFF0C: case 0xFF0E: case 0xFF1A:
    case 0xFF1B: case 0xFF01: case 0xFF1F: case 0xFF05: case 0x2026:
    case 0x2025: case 0xFF09: case 0x3011: case 0x300B: case 0x300D:
    case 0x300F: case 0x3009: case 0x201D: case 0x2019: case 0xFF5D:
    case 0x3015: case 0x30FB: case 0x2014:
      return true;
    default:
      return false;
  }
}

// Punctuation that must not end a line (opening marks).
inline bool is_no_line_end(std::uint32_t cp) {
  switch (cp) {
    case 0xFF08: case 0x3010: case 0x300A: case 0x300C: case 0x300E:
    case 0x3008: case 0x201C: case 0x2018: case 0xFF5B: case 0x3014:
      return true;
    default:
      return false;
  }
}

// Scripts that need complex shaping (Arabic, Indic, SEA, ...). Reported,
// not shaped: this tool measures without a shaping engine.
inline bool needs_shaping(std::uint32_t cp) {
  return in(cp, 0x0600, 0x06FF) || in(cp, 0x0750, 0x077F) ||
         in(cp, 0x0900, 0x097F) || in(cp, 0x0980, 0x09FF) ||
         in(cp, 0x0A00, 0x0A7F) || in(cp, 0x0A80, 0x0AFF) ||
         in(cp, 0x0B00, 0x0FFF) || in(cp, 0x1780, 0x17FF) ||
         in(cp, 0x0E00, 0x0E7F) || in(cp, 0x1000, 0x109F);
}

}  // namespace subtitle::utf8
