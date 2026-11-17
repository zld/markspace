#pragma once

#include <string>

#include "manifest.hpp"

namespace tl {

struct RenderOptions {
  std::string out_dir;
  bool verify = false;
};

// Renders the timeline to PPM frames and prints per-transition stats.
// Returns a process exit code: 0 ok, 3 timeline invalid, 4 I/O failure,
// 5 frame consistency failure.
int run_render(const Manifest& m, const RenderOptions& opt);

}  // namespace tl
