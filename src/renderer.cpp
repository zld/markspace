#include "renderer.hpp"

#include <algorithm>

#include "errors.hpp"
#include "transition.hpp"

namespace tr {

namespace {

std::uint8_t clampByte(int v) {
  if (v < 0) return 0;
  if (v > 255) return 255;
  return static_cast<std::uint8_t>(v);
}

// floor(time) as int; source time is non-negative by construction.
std::int64_t floorTime(const Rational& t) {
  return t.num() / t.den();
}

// (time * scale) mod modulus for scrolling patterns, all integer exact.
std::int64_t timeMod(const Rational& t, std::int64_t scale,
                     std::int64_t modulus) {
  const Rational v = t * Rational(scale, 1);
  return ((v.num() / v.den()) % modulus + modulus) % modulus;
}

void gradientMaterial(const Rational& t, FrameBuffer& fb) {
  const std::int64_t scroll = timeMod(t, 1, 64);
  for (int y = 0; y < fb.height(); ++y) {
    for (int x = 0; x < fb.width(); ++x) {
      fb.setPixel(x, y, clampByte((x * 255) / std::max(1, fb.width() - 1)),
                  clampByte((y * 255) / std::max(1, fb.height() - 1)),
                  clampByte(static_cast<int>(scroll) * 4));
    }
  }
}

void checkerMaterial(const Rational& t, FrameBuffer& fb) {
  const std::int64_t shift = timeMod(t, 1, 16);
  const int cell = std::max(4, std::min(fb.width(), fb.height()) / 12);
  for (int y = 0; y < fb.height(); ++y) {
    for (int x = 0; x < fb.width(); ++x) {
      const bool on = (((x + shift) / cell) + ((y + shift) / cell)) % 2 == 0;
      if (on) {
        fb.setPixel(x, y, 230, 230, 220);
      } else {
        fb.setPixel(x, y, 30, 40, 70);
      }
    }
  }
}

void coordsMaterial(const Rational& t, FrameBuffer& fb) {
  // Encodes coordinates and source time directly into channels, giving the
  // regression tests an exact, position-identifiable pattern.
  const std::int64_t tt = floorTime(t);
  for (int y = 0; y < fb.height(); ++y) {
    for (int x = 0; x < fb.width(); ++x) {
      fb.setPixel(x, y, clampByte(x & 0xFF), clampByte(y & 0xFF),
                  clampByte(static_cast<int>(tt & 0xFF)));
    }
  }
}

std::uint8_t mixChannel(std::uint8_t a, std::uint8_t b,
                        const Weight& w) {
  const __int128_t v =
      (static_cast<__int128_t>(a) * w.aWeight +
       static_cast<__int128_t>(b) * w.bWeight) /
      w.den;
  return static_cast<std::uint8_t>(v);
}

}  // namespace

void renderMaterial(Material m, const Rational& time, FrameBuffer& fb) {
  switch (m) {
    case Material::Gradient: gradientMaterial(time, fb); return;
    case Material::Checker: checkerMaterial(time, fb); return;
    case Material::Coords: coordsMaterial(time, fb); return;
  }
  throw UsageError("unknown material");
}

Renderer::Renderer(const Timeline& tl) : tl_(tl) {}

const Clip& Renderer::clipAt(std::int64_t g) const {
  for (const Clip& c : tl_.clips) {
    if (g >= c.first && g < c.last) return c;
  }
  throw UsageError("frame " + std::to_string(g) +
                   " is outside the validated timeline");
}

void Renderer::renderFrame(std::int64_t g, FrameBuffer& fb) const {
  if (g < 0 || g >= tl_.totalFrames) {
    throw UsageError("frame " + std::to_string(g) + " out of range [0," +
                     std::to_string(tl_.totalFrames) + ")");
  }
  if (fb.width() != tl_.width || fb.height() != tl_.height) {
    throw UsageError("frame buffer does not match timeline dimensions");
  }

  const Transition* trn = nullptr;
  for (const Transition& t : tl_.transitions) {
    if (g >= t.first && g < t.last) {
      trn = &t;
      break;
    }
  }
  if (trn == nullptr) {
    const Clip& c = clipAt(g);
    renderMaterial(c.material, sourceTimeAt(c, g), fb);
    return;
  }

  // Transition frames: progress comes from GLOBAL time so crossing a speed
  // boundary never resets it; source times still accumulate per clip.
  const Clip& ca = clipAt(trn->first);
  const Clip& cb = clipAt(trn->last - 1);
  FrameBuffer bufB(tl_.width, tl_.height);
  renderMaterial(ca.material, sourceTimeAt(ca, g), fb);
  renderMaterial(cb.material, sourceTimeAt(cb, g), bufB);

  const std::int64_t span = trn->last - trn->first;
  const std::int64_t pNum = g - trn->first;  // half-open: never reaches span
  for (int y = 0; y < tl_.height; ++y) {
    for (int x = 0; x < tl_.width; ++x) {
      KernelParams kp{trn->type, x, y, tl_.width, tl_.height, pNum, span};
      const Weight w = pixelWeight(kp);
      std::uint8_t ar, ag, ab, br, bg, bb;
      fb.getPixel(x, y, &ar, &ag, &ab);
      bufB.getPixel(x, y, &br, &bg, &bb);
      fb.setPixel(x, y, mixChannel(ar, br, w), mixChannel(ag, bg, w),
                  mixChannel(ab, bb, w));
    }
  }
}

}  // namespace tr
