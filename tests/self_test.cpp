// Self-contained regression tests. They call the exact same timeline
// validation, source-time mapping, transition kernels and renderer as the
// production binary; no test-only reimplementations.
#include <cstdio>
#include <functional>
#include <string>
#include <vector>

#include "frame_buffer.hpp"
#include "hash.hpp"
#include "renderer.hpp"
#include "sample.hpp"
#include "timeline.hpp"
#include "transition.hpp"

namespace {

using namespace tr;

int g_failed = 0;
int g_checked = 0;

void check(bool cond, const std::string& name) {
  ++g_checked;
  if (!cond) {
    ++g_failed;
    std::printf("FAIL: %s\n", name.c_str());
  }
}

void throwsUsage(std::function<void()> fn, const std::string& name) {
  bool caught = false;
  try {
    fn();
  } catch (const UsageError&) {
    caught = true;
  }
  check(caught, name);
}

Timeline baseTimeline() {
  Timeline tl;
  tl.width = 24;
  tl.height = 16;
  tl.fps = Rational(30, 1);
  tl.totalFrames = 10;
  Clip c;
  c.first = 0;
  c.last = 10;
  c.material = Material::Coords;
  c.segments.push_back(SpeedSegment{0, 10, Rational(1, 1)});
  tl.clips.push_back(c);
  return tl;
}

void testRational() {
  check(Rational(2, 4) == Rational(1, 2), "rational reduction");
  check(Rational(1, 3) + Rational(1, 6) == Rational(1, 2), "rational add");
  check(Rational(3, 4) - Rational(1, 4) == Rational(1, 2), "rational sub");
  check(Rational(2, 3) * Rational(3, 4) == Rational(1, 2), "rational mul");
  check(Rational(2, 3) / Rational(4, 3) == Rational(1, 2), "rational div");
  check(Rational(1, 3) < Rational(1, 2), "rational compare");
  throwsUsage([] { Rational(1, 0); }, "zero denominator rejected");
  throwsUsage([] {
    Rational big(INT64_MAX, 1);
    (void)(big + big);
  }, "rational overflow detected");
}

void testTimelineValidation() {
  Timeline ok = baseTimeline();
  validateTimeline(ok);  // must not throw
  check(true, "valid timeline accepted");

  Timeline negative = ok;
  negative.clips[0].first = -1;
  negative.clips[0].segments[0].first = -1;
  throwsUsage([&] { validateTimeline(negative); }, "negative time rejected");

  Timeline gap = ok;
  gap.clips[0].first = 1;
  gap.clips[0].segments[0].first = 1;
  throwsUsage([&] { validateTimeline(gap); }, "frame gap rejected");

  Clip tail;
  tail.first = 5;
  tail.last = 10;
  tail.material = Material::Gradient;
  tail.segments.push_back(SpeedSegment{5, 10, Rational(1, 1)});
  Timeline overlap = ok;
  overlap.clips.push_back(tail);
  throwsUsage([&] { validateTimeline(overlap); }, "overlapping clips rejected");

  Timeline zeroTr = ok;
  zeroTr.transitions.push_back(Transition{3, 3, TType::Linear});
  throwsUsage([&] { validateTimeline(zeroTr); },
              "zero-duration transition rejected");

  Timeline badSpeed = ok;
  badSpeed.clips[0].segments[0].speed = Rational(0, 1);
  throwsUsage([&] { validateTimeline(badSpeed); }, "zero speed rejected");

  Timeline negSpeed = ok;
  negSpeed.clips[0].segments[0].speed = Rational(-2, 1);
  throwsUsage([&] { validateTimeline(negSpeed); }, "negative speed rejected");

  Timeline trOverlap = ok;
  trOverlap.transitions = {{2, 6, TType::Linear}, {5, 8, TType::Wipe}};
  throwsUsage([&] { validateTimeline(trOverlap); },
              "overlapping transitions rejected");

  Timeline segGap = ok;
  segGap.clips[0].segments = {{0, 4, Rational(1, 1)},
                              {5, 10, Rational(1, 1)}};
  throwsUsage([&] { validateTimeline(segGap); },
              "speed segment gap rejected");

  Timeline oversize = ok;
  oversize.width = 5000;
  throwsUsage([&] { validateTimeline(oversize); }, "oversize rejected");
}

void testSourceTime() {
  Clip c;
  c.first = 0;
  c.last = 12;
  c.material = Material::Gradient;
  c.segments = {{0, 4, Rational(1, 1)},
                {4, 8, Rational(3, 2)},
                {8, 12, Rational(1, 2)}};
  Timeline tl;
  tl.width = 8;
  tl.height = 8;
  tl.fps = Rational(30, 1);
  tl.totalFrames = 12;
  tl.clips.push_back(c);
  validateTimeline(tl);
  check(sourceTimeAt(c, 0) == Rational(0, 1), "source time starts at zero");
  check(sourceTimeAt(c, 4) == Rational(4, 1), "first segment accumulated");
  check(sourceTimeAt(c, 8) == Rational(10, 1), "3/2 segment accumulated");
  check(sourceTimeAt(c, 12) == Rational(12, 1), "final 1/2 segment total");
  check(sourceTimeStart(c) == Rational(0, 1), "clip start source time");
}

void testKernelEndpoints() {
  KernelParams p{TType::Linear, 0, 0, 4, 4, 0, 8};
  check(pixelWeight(p) == Weight{8, 0, 8}, "linear progress 0 is all A");
  p.progressNum = 7;
  check(pixelWeight(p) == Weight{1, 7, 8}, "linear interior weight exact");

  KernelParams wipe{TType::Wipe, 0, 0, 4, 4, 8, 8};
  // At t=1 the leftmost column (x+1=1) is strictly inside the B region.
  check(pixelWeight(wipe).bWeight == pixelWeight(wipe).den,
        "wipe B-side pixel fully B");
  KernelParams wipeEdge{TType::Wipe, 3, 0, 4, 4, 8, 8};
  // Rightmost column at t=1 is the anti-aliased boundary, still partly A.
  check(pixelWeight(wipeEdge).aWeight > 0 &&
            pixelWeight(wipeEdge).bWeight > 0,
        "wipe boundary column is anti-aliased");
  KernelParams wipe0{TType::Wipe, 0, 0, 4, 4, 0, 8};
  check(pixelWeight(wipe0).aWeight == pixelWeight(wipe0).den,
        "wipe at progress 0 is all A everywhere");

  KernelParams circle0{TType::Circle, 0, 0, 8, 8, 0, 8};
  check(pixelWeight(circle0).aWeight == pixelWeight(circle0).den,
        "circle progress 0 is all A");

  KernelParams chk0{TType::CheckerBoard, 0, 0, 16, 16, 0, 8};
  check(pixelWeight(chk0).aWeight == pixelWeight(chk0).den,
        "checker progress 0 is all A");

  throwsUsage([] {
    KernelParams bad{TType::Linear, 0, 0, 4, 4, 9, 8};
    pixelWeight(bad);
  }, "kernel rejects progress above 1");
}

void testRendererAndPixels() {
  const auto scenes =
      buildSample("standard", 24, 16, Rational(30, 1));
  check(scenes.size() == 4, "four sample scenes built");
  for (const SampleScene& sc : scenes) {
    const Renderer r(sc.timeline);
    FrameBuffer first(24, 16);
    FrameBuffer again(24, 16);
    r.renderFrame(0, first);
    r.renderFrame(0, again);
    check(hashFrame(first) == hashFrame(again),
          std::string("deterministic frame: ") + transitionName(sc.type));
    FrameBuffer last(24, 16);
    r.renderFrame(sc.timeline.totalFrames - 1, last);
    check(hashFrame(first) != hashFrame(last),
          std::string("first/last differ: ") + transitionName(sc.type));
  }

  // Half-open coverage: every frame 0..29 renders without gap or exception.
  const Renderer r(scenes.front().timeline);
  FrameBuffer fb(24, 16);
  std::uint64_t any = 0;
  for (std::int64_t g = 0; g < 30; ++g) {
    r.renderFrame(g, fb);
    any ^= hashFrame(fb);
  }
  check(any != 0, "all 30 half-open frames rendered");
  throwsUsage([&] { r.renderFrame(30, fb); }, "frame past end rejected");

  // Coords material encodes x,y directly: deterministic and position-exact.
  Timeline tl = baseTimeline();
  Renderer plain(tl);
  plain.renderFrame(2, fb);
  std::uint8_t rr, gg, bb;
  fb.getPixel(7, 9, &rr, &gg, &bb);
  check(rr == 7 && gg == 9 && bb == 2, "coords material encodes position");
}

void testPpmRoundTrip() {
  FrameBuffer fb(5, 3);
  for (int y = 0; y < 3; ++y)
    for (int x = 0; x < 5; ++x)
      fb.setPixel(x, y, static_cast<std::uint8_t>(x * 50),
                  static_cast<std::uint8_t>(y * 80), 12);
  const std::string dir = "/tmp/transition-selftest-io";
  ensureOutputDir(dir);
  const std::string path = dir + "/roundtrip.ppm";
  writePpmAtomic(path, fb);
  const FrameBuffer back = readPpm(path);
  check(hashFrame(fb) == hashFrame(back), "PPM P6 round trip preserves pixels");
  const FrameDiff d = compareFrames(fb, back);
  check(d.diffPixels == 0 && d.sumAbsDiff == 0, "identical frames zero diff");
  check(formatMeanAbs(d, 5, 3) == "0.0000", "zero mean formats as 0.0000");
  throwsUsage([] { FrameBuffer bad(0, 4); }, "zero width rejected");
  throwsUsage([] { FrameBuffer bad(4097, 4); }, "dimension cap enforced");
}

}  // namespace

int main() {
  testRational();
  testTimelineValidation();
  testSourceTime();
  testKernelEndpoints();
  testRendererAndPixels();
  testPpmRoundTrip();
  std::printf("self-test: %d checks, %d failed\n", g_checked, g_failed);
  return g_failed == 0 ? 0 : 1;
}
