#include "transition.hpp"

#include <algorithm>
#include <cstdlib>

namespace tl {

namespace {

constexpr std::int64_t kScale = kFpScale;

std::uint8_t blend_byte(std::uint8_t a, std::uint8_t b, std::int64_t alpha) {
    const std::int64_t value =
        (static_cast<std::int64_t>(a) * (kScale - alpha) +
         static_cast<std::int64_t>(b) * alpha) /
        kScale;
    return static_cast<std::uint8_t>(value);
}

void blend_pixel(FrameBuffer& out, const FrameBuffer& from,
                 const FrameBuffer& to, int x, int y, std::int64_t alpha) {
    const Pixel& a = from.at(x, y);
    const Pixel& b = to.at(x, y);
    out.at(x, y) = Pixel{blend_byte(a.r, b.r, alpha),
                         blend_byte(a.g, b.g, alpha),
                         blend_byte(a.b, b.b, alpha)};
}

void render_dissolve(FrameBuffer& out, const FrameBuffer& from,
                     const FrameBuffer& to, std::int64_t p_fp) {
    for (int y = 0; y < from.height(); ++y) {
        for (int x = 0; x < from.width(); ++x) {
            blend_pixel(out, from, to, x, y, p_fp);
        }
    }
}

// Alpha for a one-dimensional edge at fixed-point position q with a soft
// band of total width `soft`. `pos` is the fixed-point pixel coordinate.
std::int64_t edge_alpha(std::int64_t pos, std::int64_t q,
                        std::int64_t soft) {
    if (soft <= 0) return pos >= q ? kScale : 0;
    const std::int64_t half = soft / 2;
    if (pos <= q - half) return 0;
    if (pos >= q + half) return kScale;
    return ((pos - (q - half)) * kScale) / soft;
}

void render_wipe(FrameBuffer& out, const FrameBuffer& from,
                 const FrameBuffer& to, std::int64_t p_fp,
                 const TransitionParams& params) {
    bool horizontal = true;
    bool forward = true;
    int extent = from.width();
    switch (params.direction) {
        case WipeDirection::LeftToRight:
            horizontal = true; forward = true; extent = from.width(); break;
        case WipeDirection::RightToLeft:
            horizontal = true; forward = false; extent = from.width(); break;
        case WipeDirection::TopToBottom:
            horizontal = false; forward = true; extent = from.height(); break;
        case WipeDirection::BottomToTop:
            horizontal = false; forward = false; extent = from.height(); break;
    }
    const std::int64_t q = p_fp * extent;
    const std::int64_t soft =
        (static_cast<std::int64_t>(extent) * kScale *
         params.softness_num) /
        params.softness_den;

    for (int y = 0; y < from.height(); ++y) {
        for (int x = 0; x < from.width(); ++x) {
            const int coord = horizontal ? x : y;
            const int slot = forward ? coord : extent - 1 - coord;
            const std::int64_t pos = static_cast<std::int64_t>(slot) * kScale;
            blend_pixel(out, from, to, x, y, edge_alpha(pos, q, soft));
        }
    }
}

void render_circle(FrameBuffer& out, const FrameBuffer& from,
                   const FrameBuffer& to, std::int64_t p_fp,
                   const TransitionParams& params) {
    const int w = from.width();
    const int h = from.height();
    const std::int64_t cx_fp = (static_cast<std::int64_t>(w - 1) * kScale) / 2;
    const std::int64_t cy_fp = (static_cast<std::int64_t>(h - 1) * kScale) / 2;
    const std::uint64_t rmax_fp = isqrt64(
        static_cast<std::uint64_t>(cx_fp * cx_fp + cy_fp * cy_fp));
    const std::int64_t radius =
        p_fp * static_cast<std::int64_t>(rmax_fp) / kScale;
    const std::int64_t soft =
        (static_cast<std::int64_t>(rmax_fp) * params.softness_num) /
        params.softness_den;

    for (int y = 0; y < h; ++y) {
        for (int x = 0; x < w; ++x) {
            const std::int64_t dx =
                static_cast<std::int64_t>(x) * kScale - cx_fp;
            const std::int64_t dy =
                static_cast<std::int64_t>(y) * kScale - cy_fp;
            const std::uint64_t dist =
                isqrt64(static_cast<std::uint64_t>(dx * dx + dy * dy));
            std::int64_t alpha;
            if (soft <= 0) {
                alpha = static_cast<std::int64_t>(dist) >= radius ? kScale : 0;
            } else {
                const std::int64_t half = soft / 2;
                const std::int64_t d = static_cast<std::int64_t>(dist);
                if (d <= radius - half) alpha = 0;
                else if (d >= radius + half) alpha = kScale;
                else alpha = ((d - (radius - half)) * kScale) / soft;
            }
            blend_pixel(out, from, to, x, y, alpha);
        }
    }
}

}  // namespace

std::uint64_t isqrt64(std::uint64_t value) {
    std::uint64_t guess = 0;
    std::uint64_t bit = 1ULL << 62;
    while (bit > value) bit >>= 2;
    while (bit != 0) {
        if (value >= guess + bit) {
            value -= guess + bit;
            guess = (guess >> 1) + bit;
        } else {
            guess >>= 1;
        }
        bit >>= 2;
    }
    return guess;
}

const char* transition_type_name(TransitionType type) {
    switch (type) {
        case TransitionType::Dissolve: return "dissolve";
        case TransitionType::Wipe: return "wipe";
        case TransitionType::CircleReveal: return "circle_reveal";
    }
    return "unknown";
}

bool render_transition(FrameBuffer& out, const FrameBuffer& from,
                       const FrameBuffer& to, int progress_num,
                       int progress_den, TransitionType type,
                       const TransitionParams& params,
                       std::string* error) {
    auto fail = [&](const char* msg) {
        if (error) *error = msg;
        return false;
    };
    if (!from.valid() || !to.valid() || !out.valid())
        return fail("invalid frame buffer");
    if (!from.dimensions_match(to) || !out.dimensions_match(from))
        return fail("frame dimension mismatch");
    if (progress_den <= 0 || progress_num < 0 ||
        progress_num > progress_den)
        return fail("transition progress outside [0,1]");
    if (params.softness_den <= 0 || params.softness_num < 0 ||
        params.softness_num > params.softness_den)
        return fail("invalid softness fraction");

    // Endpoints are exact copies, guaranteeing first/last transition
    // frames correspond precisely to the two source clips.
    if (progress_num == 0) { out = from; return true; }
    if (progress_num == progress_den) { out = to; return true; }

    const std::int64_t p_fp =
        static_cast<std::int64_t>(progress_num) * kScale / progress_den;
    switch (type) {
        case TransitionType::Dissolve:
            render_dissolve(out, from, to, p_fp);
            break;
        case TransitionType::Wipe:
            render_wipe(out, from, to, p_fp, params);
            break;
        case TransitionType::CircleReveal:
            render_circle(out, from, to, p_fp, params);
            break;
    }
    return true;
}

}  // namespace tl
