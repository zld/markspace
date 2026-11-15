#pragma once
#include <string>
#include <vector>

#include "ass_model.hpp"
#include "ass_renderer.hpp"

namespace assaudit {

struct LayoutConfig {
  int width = 1920;
  int height = 1080;
  bool strict = false;
};

struct LayoutOutcome {
  std::vector<EventReport> reports;
  int rendered = 0;
  int blocked_events = 0;
  bool structural_error = false;
  bool render_failure = false;
  std::vector<std::string> jsonl;  // one JSON object per processed event
};

// Run diagnostics, tag resolution and offscreen libass rendering over
// every dialogue event. Renderer must already be initialised.
LayoutOutcome runLayout(const AssDocument& doc, AssRenderer& renderer,
                        const LayoutConfig& cfg);

}  // namespace assaudit
