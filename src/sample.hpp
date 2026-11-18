// Built-in sample timelines. All sample data is generated in code; nothing
// is read from external asset files.
#pragma once

#include <string>
#include <vector>

#include "timeline.hpp"

namespace tr {

struct SampleScene {
  TType type;
  Timeline timeline;
};

// `sample`: "standard" (unit speed) or "speed" (rational variable speed that
// crosses the transition boundary, exercising global-time progress).
// Each scene is 30 frames: two clips, one transition covering all frames.
std::vector<SampleScene> buildSample(const std::string& sample, int width,
                                     int height, const Rational& fps);

}  // namespace tr
