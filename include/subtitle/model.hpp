#pragma once

#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace subtitle {

// All font metrics are expressed in 26.6 fixed-point pixel units.
using Fixed = std::int64_t;

struct BBox {
  Fixed x_min = 0, y_min = 0, x_max = 0, y_max = 0;
  Fixed width() const { return x_max - x_min; }
  Fixed height() const { return y_max - y_min; }
  bool empty() const { return x_max <= x_min || y_max <= y_min; }
  void unite(const BBox& o) {
    if (o.empty()) return;
    if (empty()) { *this = o; return; }
    if (o.x_min < x_min) x_min = o.x_min;
    if (o.y_min < y_min) y_min = o.y_min;
    if (o.x_max > x_max) x_max = o.x_max;
    if (o.y_max > y_max) y_max = o.y_max;
  }
};

// One parsed SRT cue. `text` keeps original line breaks as '\n'.
struct Cue {
  std::size_t index = 0;        // 0-based order in the file
  std::int64_t start_ms = 0;
  std::int64_t end_ms = 0;
  std::string text;             // sanitized UTF-8, '\n' separated
  std::size_t source_line = 0;  // 1-based line of the cue index
};

struct ParseError {
  std::size_t line = 0;  // 1-based source line
  std::string message;
};

struct ParseResult {
  std::vector<Cue> cues;
  std::vector<ParseError> errors;
  std::uint64_t replaced_chars = 0;  // invalid UTF-8 bytes replaced by U+FFFD
};

struct LayoutParams {
  Fixed max_width = 0;  // 26.6 pixels
  int max_lines = 0;
};

struct LineLayout {
  std::size_t text_offset = 0;  // UTF-8 byte offset of line start in cue text
  std::size_t text_len = 0;     // bytes covered by this line
  Fixed advance_width = 0;      // pen advance of the line
  BBox ink;                     // ink bounding box, y relative to baseline 0
};

struct CueLayout {
  std::size_t cue_index = 0;
  BBox bbox;                              // union of ink boxes, 26.6
  std::vector<LineLayout> lines;
  std::vector<std::size_t> wrap_offsets;  // UTF-8 byte offsets of line starts
  std::uint32_t missing_glyphs = 0;
  bool requires_shaping = false;  // complex script present, reported not shaped
  bool overflow = false;          // any line too wide or too many lines
  bool width_overflow = false;
  bool line_overflow = false;
};

struct Summary {
  std::uint64_t cue_count = 0;
  std::uint64_t overflow_entries = 0;
  std::uint64_t parse_errors = 0;
  std::uint64_t encoding_errors = 0;  // replaced invalid UTF-8 sequences
  std::uint64_t missing_glyphs = 0;
  std::uint64_t shaping_cues = 0;
};

// Thrown on any font loading / FreeType failure (exit code 3).
struct FontError : std::runtime_error {
  using std::runtime_error::runtime_error;
};

}  // namespace subtitle
