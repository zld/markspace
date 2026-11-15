#pragma once

// Grapheme-ish cluster segmentation and dynamic-programming line breaking.
// Widths are supplied by the caller (layout engine) so this module stays
// font-agnostic.

#include <cstddef>
#include <functional>
#include <string>
#include <utility>
#include <vector>

#include "subtitle/model.hpp"

namespace subtitle {

struct Cluster {
  enum Kind : unsigned char {
    Word,        // latin/digit/symbol run member; words break atomically
    Space,       // space or tab; break opportunity, consumed at line ends
    Cjk,         // CJK character; breakable on both sides
    PunctOpen,   // opening punctuation: no break after
    PunctClose,  // closing punctuation: no break before
    Newline,     // explicit '\n': forced break
  };
  std::size_t byte_offset = 0;
  std::size_t byte_len = 0;
  std::uint32_t first_cp = 0;
  Kind kind = Word;
};

// Segment sanitized UTF-8 text into clusters (base + combining extenders).
std::vector<Cluster> segment_clusters(const std::string& text);

// Advance width of the cluster range [from, to), 26.6 units.
using WidthFn = std::function<Fixed(std::size_t from, std::size_t to)>;

struct BreakResult {
  std::vector<std::pair<std::size_t, std::size_t>> lines;  // cluster ranges
  std::vector<bool> fits;  // per line: width <= max_width
};

// DP wrap: minimizes squared slack (raggedness balance), never splits
// clusters, words or URL-like runs, honors CJK punctuation rules and
// explicit newlines. Unbreakable over-wide content forms its own line
// and is reported via `fits == false`.
BreakResult break_lines(const std::vector<Cluster>& clusters,
                        const WidthFn& width, Fixed max_width);

}  // namespace subtitle
