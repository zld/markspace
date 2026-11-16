#include "libass_renderer.hpp"

#include <ass/ass.h>

#include <algorithm>
#include <cstdarg>
#include <cstdio>
#include <thread>

namespace subaudit {
namespace {

void ass_log(int level, const char* fmt, va_list args, void* /*data*/) {
    if (level > 2) return;  // errors and warnings only
    std::fputs("render: ", stderr);
    std::vfprintf(stderr, fmt, args);
    std::fputc('\n', stderr);
}

struct Frame {
    int w = 0, h = 0;
    std::vector<unsigned char> px;   // premultiplied RGBA, w*h*4 bytes
};

// Source-over composites libass bitmaps onto the frame. Deterministic
// integer math so digests are stable across runs and thread counts.
void composite(Frame& f, const ASS_Image* img) {
    for (const ASS_Image* im = img; im; im = im->next) {
        const unsigned r = (im->color >> 24) & 0xFF;
        const unsigned g = (im->color >> 16) & 0xFF;
        const unsigned b = (im->color >> 8) & 0xFF;
        const unsigned a = 255 - (im->color & 0xFF);  // ASS alpha is inverted
        for (int yy = 0; yy < im->h; ++yy) {
            const int dy = im->dst_y + yy;
            if (dy < 0 || dy >= f.h) continue;
            const unsigned char* row =
                im->bitmap + static_cast<std::size_t>(yy) * im->stride;
            for (int xx = 0; xx < im->w; ++xx) {
                const int dx = im->dst_x + xx;
                if (dx < 0 || dx >= f.w) continue;
                const unsigned sa = static_cast<unsigned>(row[xx]) * a / 255;
                const unsigned inv = 255 - sa;
                unsigned char* d =
                    &f.px[(static_cast<std::size_t>(dy) * f.w + dx) * 4];
                d[0] = static_cast<unsigned char>((r * sa + d[0] * inv + 127) / 255);
                d[1] = static_cast<unsigned char>((g * sa + d[1] * inv + 127) / 255);
                d[2] = static_cast<unsigned char>((b * sa + d[2] * inv + 127) / 255);
                d[3] = static_cast<unsigned char>(sa + (d[3] * inv + 127) / 255);
            }
        }
    }
}

RenderedRegion analyze_frame(const Frame& f, int event_id) {
    RenderedRegion g;
    g.event_id = event_id;
    int x0 = f.w, y0 = f.h, x1 = -1, y1 = -1;
    std::uint64_t hash = 1469598103934665603ULL;  // FNV-1a 64
    for (int y = 0; y < f.h; ++y) {
        for (int x = 0; x < f.w; ++x) {
            const std::size_t o = (static_cast<std::size_t>(y) * f.w + x) * 4;
            for (int k = 0; k < 4; ++k) {
                hash ^= f.px[o + k];
                hash *= 1099511628211ULL;
            }
            if (f.px[o + 3] != 0) {
                x0 = std::min(x0, x);
                y0 = std::min(y0, y);
                x1 = std::max(x1, x);
                y1 = std::max(y1, y);
            }
        }
    }
    if (x1 < 0) return g;  // zero visible pixels -> empty region
    g.x = x0;
    g.y = y0;
    g.w = x1 - x0 + 1;
    g.h = y1 - y0 + 1;
    hash ^= hash >> 32;
    g.pixel_digest = static_cast<std::uint16_t>(hash ^ (hash >> 16));
    return g;
}

// One worker: owns its libass library + renderer, renders its share of
// events. libass contexts are not shared across threads.
class Worker {
  public:
    bool run(const std::vector<LayoutInput>& inputs,
             const std::vector<std::size_t>& mine, int w, int h,
             std::vector<RenderedRegion>& out) {
        ASS_Library* lib = ass_library_init();
        if (!lib) {
            err_ = "ass_library_init failed";
            return false;
        }
        ass_set_message_cb(lib, ass_log, nullptr);
        ASS_Renderer* ren = ass_renderer_init(lib);
        if (!ren) {
            err_ = "ass_renderer_init failed";
            ass_library_done(lib);
            return false;
        }
        ass_set_frame_size(ren, w, h);
        ass_set_fonts(ren, nullptr, "Arial", ASS_FONTPROVIDER_AUTODETECT, nullptr, 1);
        Frame frame;
        frame.w = w;
        frame.h = h;
        frame.px.assign(static_cast<std::size_t>(w) * h * 4, 0);
        bool ok = true;
        for (const std::size_t idx : mine) {
            const LayoutInput& li = inputs[idx];
            ASS_Track* track =
                ass_read_memory(lib, const_cast<char*>(li.ass.data()), li.ass.size(),
                                const_cast<char*>("UTF-8"));
            if (!track) {
                err_ = "failed to build track for event " + std::to_string(li.event_id);
                ok = false;
                break;
            }
            std::fill(frame.px.begin(), frame.px.end(), 0);
            int change = 0;
            ASS_Image* img =
                ass_render_frame(ren, track, static_cast<int>(li.mid_ms), &change);
            composite(frame, img);
            out[idx] = analyze_frame(frame, li.event_id);
            ass_free_track(track);
        }
        ass_renderer_done(ren);
        ass_library_done(lib);
        return ok;
    }

    std::string err_;
};

}  // namespace

bool render_regions(const std::vector<LayoutInput>& inputs, int canvas_w, int canvas_h,
                    int jobs, std::vector<RenderedRegion>& out, std::string& err) {
    out.assign(inputs.size(), RenderedRegion{});
    if (inputs.empty()) return true;
    const int nthreads =
        std::max(1, std::min(jobs, static_cast<int>(inputs.size())));
    std::vector<std::vector<std::size_t>> parts(static_cast<std::size_t>(nthreads));
    for (std::size_t i = 0; i < inputs.size(); ++i)
        parts[i % static_cast<std::size_t>(nthreads)].push_back(i);
    std::vector<Worker> workers(static_cast<std::size_t>(nthreads));
    std::vector<std::thread> threads;
    threads.reserve(static_cast<std::size_t>(nthreads - 1));
    for (int t = 1; t < nthreads; ++t)
        threads.emplace_back([&, t] {
            workers[static_cast<std::size_t>(t)].run(
                inputs, parts[static_cast<std::size_t>(t)], canvas_w, canvas_h, out);
        });
    workers[0].run(inputs, parts[0], canvas_w, canvas_h, out);
    for (auto& th : threads) th.join();
    for (const auto& wk : workers)
        if (!wk.err_.empty()) {
            err = wk.err_;
            return false;
        }
    return true;
}

}  // namespace subaudit
