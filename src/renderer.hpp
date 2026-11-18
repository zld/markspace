// Maps global frame numbers onto clips/transitions and renders deterministic
// RGB frames. Material evaluation and blending are integer-only; transition
// mixing always goes through the shared kernels in transition.cpp.
#pragma once

#include <cstdint>

#include "frame_buffer.hpp"
#include "timeline.hpp"

namespace tr {

class Renderer {
 public:
  explicit Renderer(const Timeline& tl);

  // Renders global frame g into fb (must match the timeline dimensions).
  void renderFrame(std::int64_t g, FrameBuffer& fb) const;

  // The clip active at global frame g under the half-open tiling rule.
  const Clip& clipAt(std::int64_t g) const;

 private:
  const Timeline& tl_;
};

// Deterministic procedural materials; `time` is accumulated source time.
void renderMaterial(Material m, const Rational& time, FrameBuffer& fb);

}  // namespace tr
