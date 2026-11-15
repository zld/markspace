#pragma once

// Shared layout interface used by the CLI, the exporter and the tests.
// One LayoutEngine instance is NOT thread-safe; create one per worker.

#include <memory>
#include <string>
#include <vector>

#include "subtitle/model.hpp"

namespace subtitle {

class LayoutEngine {
 public:
  // font_paths are tried in order for each codepoint (fallback chain).
  // font_size_px is the nominal pixel size. Throws FontError on failure.
  LayoutEngine(const std::vector<std::string>& font_paths, double font_size_px);
  ~LayoutEngine();

  LayoutEngine(const LayoutEngine&) = delete;
  LayoutEngine& operator=(const LayoutEngine&) = delete;

  // Lay out one cue: wrap, measure ink boxes, flag overflow.
  CueLayout layout(const Cue& cue, const LayoutParams& params) const;

 private:
  struct Impl;
  std::unique_ptr<Impl> impl_;
};

}  // namespace subtitle
