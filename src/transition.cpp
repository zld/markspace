#include "transition.hpp"

#include "errors.hpp"

namespace tl {

bool parse_transition(const std::string& name, TransitionType& out) {
  if (name == "fade") {
    out = TransitionType::Fade;
  } else if (name == "wipe") {
    out = TransitionType::Wipe;
  } else if (name == "iris") {
    out = TransitionType::Iris;
  } else {
    return false;
  }
  return true;
}

namespace {

// 8.8 fixed-point blend: w in [0,256], 0 -> a, 256 -> b.
inline uint8_t blend8(uint8_t a, uint8_t b, int w) {
  return static_cast<uint8_t>((a * (256 - w) + b * w + 128) >> 8);
}

inline int clamp256(int64_t v) { return v < 0 ? 0 : (v > 256 ? 256 : static_cast<int>(v)); }

int fade_weight(int /*x*/, int /*y*/, int /*w*/, int /*h*/, int64_t num,
                int64_t den) {
  return static_cast<int>((num * 256) / den);
}

// Left-to-right wipe with a soft edge band. The wipe front travels from
// -edge to width-1+edge so both endpoints are exact.
int wipe_weight(int x, int /*y*/, int width, int /*height*/, int64_t num,
                int64_t den) {
  const int64_t edge = width / 16 + 2;
  const int64_t pos_num = num * (width - 1 + 2 * edge) - edge * den;  // over den
  const int64_t wnum = (pos_num - static_cast<int64_t>(x) * den) * 256;
  return clamp256(wnum / (den * edge));
}

// Circular reveal from the frame center. Coordinates are doubled so the
// center is exact for even and odd sizes; distances stay squared integers.
int iris_weight(int x, int y, int width, int height, int64_t num, int64_t den) {
  const int64_t dx = 2 * x - (width - 1);
  const int64_t dy = 2 * y - (height - 1);
  const int64_t d2 = dx * dx + dy * dy;
  const int64_t max2 = static_cast<int64_t>(width - 1) * (width - 1) +
                       static_cast<int64_t>(height - 1) * (height - 1);
  const int64_t band = max2 / 16 + 1;
  // Threshold travels from -band to max2+band so both endpoints are exact.
  const int64_t thresh = (num * (max2 + 2 * band)) / den - band;
  return clamp256((thresh - d2) * 256 / band + 128);
}

using WeightFn = int (*)(int, int, int, int, int64_t, int64_t);

WeightFn weight_fn_for(TransitionType type) {
  switch (type) {
    case TransitionType::Fade: return &fade_weight;
    case TransitionType::Wipe: return &wipe_weight;
    case TransitionType::Iris: return &iris_weight;
  }
  return &fade_weight;
}

}  // namespace

void apply_transition(TransitionType type, const FrameBuffer& from,
                      const FrameBuffer& to, int64_t progress_num,
                      int64_t progress_den, FrameBuffer& out) {
  if (progress_den <= 0 || progress_num < 0 || progress_num > progress_den) {
    throw ArgError("transition progress out of range: " +
                   std::to_string(progress_num) + "/" +
                   std::to_string(progress_den));
  }
  if (from.width() != to.width() || from.height() != to.height()) {
    throw DataError("apply_transition: endpoint size mismatch");
  }
  const int w = from.width();
  const int h = from.height();
  if (out.width() != w || out.height() != h) out = FrameBuffer(w, h);

  const WeightFn weight_fn = weight_fn_for(type);
  for (int y = 0; y < h; ++y) {
    for (int x = 0; x < w; ++x) {
      const int weight = weight_fn(x, y, w, h, progress_num, progress_den);
      const uint8_t* a = from.pixel(x, y);
      const uint8_t* b = to.pixel(x, y);
      uint8_t* o = out.pixel(x, y);
      o[0] = blend8(a[0], b[0], weight);
      o[1] = blend8(a[1], b[1], weight);
      o[2] = blend8(a[2], b[2], weight);
    }
  }
}

}  // namespace tl
