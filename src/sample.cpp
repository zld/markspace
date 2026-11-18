#include "sample.hpp"

#include "errors.hpp"

namespace tr {

namespace {

// Two clips split at frame `cut`, with speed segments tiling each clip.
Clip makeClip(std::int64_t first, std::int64_t last, Material m,
              const std::vector<std::pair<std::int64_t, Rational>>& changes) {
  Clip c;
  c.first = first;
  c.last = last;
  c.material = m;
  std::int64_t cur = first;
  for (const auto& [boundary, speed] : changes) {
    c.segments.push_back(SpeedSegment{cur, boundary, speed});
    cur = boundary;
  }
  c.segments.push_back(SpeedSegment{cur, last, Rational(1, 1)});
  return c;
}

Timeline wrapScene(int width, int height, const Rational& fps,
                   std::int64_t total, Clip a, Clip b, Transition trn) {
  Timeline tl;
  tl.width = width;
  tl.height = height;
  tl.fps = fps;
  tl.totalFrames = total;
  tl.clips = {std::move(a), std::move(b)};
  tl.transitions = {std::move(trn)};
  validateTimeline(tl);
  return tl;
}

}  // namespace

std::vector<SampleScene> buildSample(const std::string& sample, int width,
                                     int height, const Rational& fps) {
  constexpr std::int64_t kFrames = 30;
  constexpr std::int64_t kCut = 15;
  const Material ma = Material::Gradient;
  const Material mb = Material::Checker;

  std::vector<SampleScene> scenes;
  for (TType type :
       {TType::Linear, TType::Wipe, TType::Circle, TType::CheckerBoard}) {
    Clip a;
    Clip b;
    if (sample == "standard") {
      a = makeClip(0, kCut, ma, {});
      b = makeClip(kCut, kFrames, mb, {});
    } else if (sample == "speed") {
      // 3/2 then 2/3 inside clip A; 1/2 then 4 inside clip B. The transition
      // spans the whole scene and therefore crosses every speed boundary.
      a = makeClip(0, kCut, ma,
                   {{5, Rational(3, 2)}, {10, Rational(2, 3)}});
      b = makeClip(kCut, kFrames, mb,
                   {{20, Rational(1, 2)}, {25, Rational(4, 1)}});
    } else {
      throw UsageError("unknown sample: " + sample +
                       " (expected 'standard' or 'speed')");
    }
    Transition trn{0, kFrames, type};
    scenes.push_back(SampleScene{
        type, wrapScene(width, height, fps, kFrames, std::move(a),
                        std::move(b), std::move(trn))});
  }
  return scenes;
}

}  // namespace tr
