#include "transition.hpp"

#include "errors.hpp"

namespace tr {

namespace {

using i128 = __int128_t;

// Weight for a threshold parameter q = qNum/qDen compared against t = tNum/tDen:
//   t <= q-1      -> full A (0, 1 below)
//   t >= q        -> full B
//   q-1 < t < q   -> one-step anti-aliased blend.
Weight linearThreshold(i128 tNum, i128 tDen, i128 qNum, i128 qDen) {
  const i128 lhs = tNum * qDen;
  const i128 tLo = (qNum - 1) * tDen;
  const i128 tHi = qNum * tDen;
  if (lhs <= tLo) return Weight{1, 0, 1};
  if (lhs >= tHi) return Weight{0, 1, 1};
  // bFrac = t - (qNum-1)/qDen, kept exact with denominator qDen*tDen.
  const i128 bNum = tNum * qDen - (qNum - 1) * tDen;
  const i128 den = tDen * qDen;
  return Weight{static_cast<std::int64_t>(den - bNum),
                static_cast<std::int64_t>(bNum),
                static_cast<std::int64_t>(den)};
}

// Stable 32-bit mix for checker cell ordering (FNV-1a over cell coords).
std::uint32_t cellHash(std::int64_t cx, std::int64_t cy) {
  std::uint32_t h = 0x811c9dc5u;
  auto eat = [&h](std::uint32_t v) {
    for (int b = 0; b < 4; ++b) {
      h ^= (v >> (b * 8)) & 0xFFu;
      h *= 0x01000193u;
    }
  };
  eat(static_cast<std::uint32_t>(cx));
  eat(static_cast<std::uint32_t>(cy));
  return h;
}

Weight linearKernel(const KernelParams& p) {
  // Uniform blend; pixel position is irrelevant.
  return Weight{p.progressDen - p.progressNum, p.progressNum, p.progressDen};
}

Weight wipeKernel(const KernelParams& p) {
  // Vertical boundary sweeps left to right: B fills columns with
  // (x+1)/w <= t, A the rest. At t=0 every column is A; the half-open end
  // frame (t=1) is never rendered, so a thin A column can persist to the
  // final output frame by construction.
  const i128 tNum = p.progressNum;
  const i128 tDen = p.progressDen;
  const i128 xNum = static_cast<i128>(p.x) + 1;  // half-open column count
  const i128 xDen = p.width;
  const i128 lhs = xNum * tDen;
  const i128 rhs = tNum * xDen;
  if (lhs <= rhs - tDen) return Weight{0, 1, 1};  // strictly inside B
  if (lhs >= rhs + tDen) return Weight{1, 0, 1};  // strictly inside A
  // One-column anti-aliasing band around the moving boundary.
  const i128 bNum = lhs - (rhs - tDen);
  const i128 den = 2 * tDen;
  return Weight{static_cast<std::int64_t>(den - bNum),
                static_cast<std::int64_t>(bNum),
                static_cast<std::int64_t>(den)};
}

Weight circleKernel(const KernelParams& p) {
  // Circle centered in the frame grows from radius 0 to the farthest corner.
  // Inside the circle is B; the boundary band is anti-aliased by radius +/-1.
  const i128 cx2 = static_cast<i128>(2 * p.x - p.width + 1);
  const i128 cy2 = static_cast<i128>(2 * p.y - p.height + 1);
  const i128 dist2 = cx2 * cx2 + cy2 * cy2;
  const i128 w = p.width;
  const i128 h = p.height;
  const i128 maxR2 = (w - 1) * (w - 1) + (h - 1) * (h - 1);
  const i128 pNum = p.progressNum;
  const i128 pDen = p.progressDen;
  // r2 = maxR2 * (pNum/pDen)^2, compared against dist2 with integer math.
  const i128 lhs = dist2 * pDen * pDen;
  const i128 r2Scaled = maxR2 * pNum * pNum;
  if (lhs <= r2Scaled - 4 * pDen * pDen) return Weight{0, 1, 1};
  if (lhs >= r2Scaled + 4 * pDen * pDen) return Weight{1, 0, 1};
  // Blend band: weight B from how far dist2 sits inside the radius.
  const i128 band = 8 * pDen * pDen;
  const i128 bNum = band - (lhs - (r2Scaled - 4 * pDen * pDen));
  const i128 clampedB = bNum < 0 ? 0 : (bNum > band ? band : bNum);
  return Weight{static_cast<std::int64_t>(band - clampedB),
                static_cast<std::int64_t>(clampedB),
                static_cast<std::int64_t>(band)};
}

Weight checkerKernel(const KernelParams& p) {
  // Interlocking cells flip A->B in a hash-stable order as progress rises.
  const int shortSide = p.width < p.height ? p.width : p.height;
  int cell = shortSide / 8;
  if (cell < 1) cell = 1;
  const std::int64_t cx = p.x / cell;
  const std::int64_t cy = p.y / cell;
  const std::uint32_t threshold = cellHash(cx, cy) % 100u;  // [0,99]
  // Cell is B once progress*100 passes its threshold; one-step edge blend.
  const i128 tNum = p.progressNum * 100;
  const i128 tDen = p.progressDen;
  const i128 qNum = threshold + 1;
  return linearThreshold(tNum, tDen, qNum, 1);
}

}  // namespace

Weight pixelWeight(const KernelParams& p) {
  if (p.progressDen <= 0 || p.progressNum < 0 ||
      p.progressNum > p.progressDen) {
    throw UsageError("transition progress out of range");
  }
  if (p.width <= 0 || p.height <= 0 || p.x < 0 || p.x >= p.width ||
      p.y < 0 || p.y >= p.height) {
    throw UsageError("transition kernel called outside frame bounds");
  }
  switch (p.type) {
    case TType::Linear: return linearKernel(p);
    case TType::Wipe: return wipeKernel(p);
    case TType::Circle: return circleKernel(p);
    case TType::CheckerBoard: return checkerKernel(p);
  }
  throw UsageError("unknown transition type in kernel");
}

}  // namespace tr
