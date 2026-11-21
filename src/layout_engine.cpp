#include "layout_engine.hpp"

#include <ass/ass.h>

#include <cstdarg>
#include <cstdio>
#include <cstring>
#include <sstream>
#include <utility>

#include "layout_internal.hpp"

namespace asspipe {

// libass log callback: route messages into a thread-unsafe but
// single-threaded capture buffer so font warnings become diagnostics.
static void libass_message_cb(int level, const char* fmt, va_list args,
                              void* data) {
    (void)level;
    auto* sink = static_cast<std::string*>(data);
    char buf[1024];
    std::vsnprintf(buf, sizeof(buf), fmt, args);
    sink->append(buf);
}

// Colour parsing is retained for future validation; libass consumes the
// raw &HAABBGGRR string itself.
[[maybe_unused]] static bool colour_parts(const std::string& ass_colour,
                                          int& a, int& b, int& g, int& r) {
    // Accept "&HAABBGGRR" with optional trailing '&'.
    std::string s = ass_colour;
    auto p = s.find('&');
    if (p == std::string::npos) return false;
    s = s.substr(p + 1);
    if (!s.empty() && (s[0] == 'H' || s[0] == 'h')) s.erase(0, 1);
    if (!s.empty() && s.back() == '&') s.pop_back();
    if (s.size() != 8) return false;
    unsigned long v = 0;
    try {
        v = std::stoul(s, nullptr, 16);
    } catch (...) {
        return false;
    }
    a = static_cast<int>((v >> 24) & 0xFF);
    b = static_cast<int>((v >> 16) & 0xFF);
    g = static_cast<int>((v >> 8) & 0xFF);
    r = static_cast<int>(v & 0xFF);
    return true;
}

std::string ass_style_line(const ResolvedStyle& rs) {
    std::ostringstream os;
    os << "Style: " << rs.name << "," << rs.fontname << "," << rs.fontsize
       << "," << rs.primary_colour << ",&H000000FF," << rs.outline_colour
       << ",&H64000000," << rs.bold << "," << rs.italic
       << ",0,0,100,100,0,0,1," << rs.outline << "," << rs.shadow << ","
       << rs.alignment << "," << rs.margin_l << "," << rs.margin_r << ","
       << rs.margin_v << ",1";
    return os.str();
}

// Compose a single-event ASS script whose [V4+ Styles] block is the
// parser's resolved data (proving parser output == libass input).
std::string build_event_script(const AssDocument& doc, const Event& event,
                               const ResolvedStyle& rs, int canvas_w,
                               int canvas_h) {
    std::ostringstream os;
    os << "[Script Info]\nScriptType: v4.00+\nPlayResX: " << canvas_w
       << "\nPlayResY: " << canvas_h << "\nWrapStyle: 0\nScaledBorderAndShadow: yes\n\n";
    os << "[V4+ Styles]\n"
       << "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
          "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
          "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
          "Alignment, MarginL, MarginR, MarginV, Encoding\n";
    os << ass_style_line(rs) << "\n\n[Events]\n"
       << "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
          "Effect, Text\n";
    // Keep the event's own override blocks verbatim so libass applies the
    // same inline tags our resolver counted.
    os << "Dialogue: 0,0:00:00.00,0:00:01.00," << rs.name
       << ",,0,0,0,," << event.text << "\n";
    (void)doc;
    return os.str();
}

// Composite one libass glyph image into an opaque RGBA canvas.
void blit_image(std::vector<std::uint8_t>& rgba, int canvas_w,
                int canvas_h, const ASS_Image* img) {
    for (const ASS_Image* im = img; im; im = im->next) {
        if (im->w <= 0 || im->h <= 0) continue;
        const unsigned char kA = (im->color >> 24) & 0xFF;
        const unsigned char kR = im->color & 0xFF;
        const unsigned char kG = (im->color >> 8) & 0xFF;
        const unsigned char kB = (im->color >> 16) & 0xFF;
        for (int y = 0; y < im->h; ++y) {
            int dy = im->dst_y + y;
            if (dy < 0 || dy >= canvas_h) continue;
            for (int x = 0; x < im->w; ++x) {
                int dx = im->dst_x + x;
                if (dx < 0 || dx >= canvas_w) continue;
                unsigned char coverage = im->bitmap[y * im->stride + x];
                if (coverage == 0) continue;
                int alpha = (coverage * (255 - kA)) / 255;
                if (alpha == 0) continue;
                std::size_t off =
                    static_cast<std::size_t>(dy) * canvas_w * 4 + dx * 4;
                rgba[off + 0] = static_cast<std::uint8_t>(
                    (kR * alpha + rgba[off + 0] * (255 - alpha)) / 255);
                rgba[off + 1] = static_cast<std::uint8_t>(
                    (kG * alpha + rgba[off + 1] * (255 - alpha)) / 255);
                rgba[off + 2] = static_cast<std::uint8_t>(
                    (kB * alpha + rgba[off + 2] * (255 - alpha)) / 255);
                rgba[off + 3] = 255;
            }
        }
    }
}

bool messages_mention_missing_font(const std::string& messages) {
    static const char* kNeedles[] = {"fontselect", "failed to find",
                                     "unable to find", "font not found"};
    for (const char* n : kNeedles)
        if (messages.find(n) != std::string::npos) return true;
    return false;
}

bool parse_canvas(const std::string& spec, Canvas& out) {
    auto x = spec.find('x');
    if (x == std::string::npos) x = spec.find('X');
    if (x == std::string::npos) return false;
    try {
        int w = std::stoi(spec.substr(0, x));
        int h = std::stoi(spec.substr(x + 1));
        if (w <= 0 || h <= 0 || w > 7680 || h > 4320) return false;
        out = Canvas{w, h};
        return true;
    } catch (...) {
        return false;
    }
}

LayoutEngine::Impl::Impl(LayoutConfig c) : cfg(std::move(c)) {
    library = ass_library_init();
    ass_set_message_cb(library, libass_message_cb, &messages);
    renderer = ass_renderer_init(library);
    ass_set_frame_size(renderer, cfg.canvas.width, cfg.canvas.height);
    ass_set_storage_size(renderer, cfg.canvas.width, cfg.canvas.height);
    ass_set_pixel_aspect(renderer, 1.0);
#if defined(__APPLE__)
    // CoreText is the default font provider; point at system families.
    ass_set_fonts_dir(library, "/System/Library/Fonts");
    if (!cfg.fonts_dir.empty())
        ass_set_fonts_dir(library, cfg.fonts_dir.c_str());
#endif
    ass_set_fonts(renderer, nullptr, "sans-serif", 1, nullptr, 1);
}

LayoutEngine::Impl::~Impl() {
    if (renderer) ass_renderer_done(renderer);
    if (library) ass_library_done(library);
}

LayoutEngine::LayoutEngine(LayoutConfig cfg)
    : impl_(new Impl(std::move(cfg))) {}

LayoutEngine::~LayoutEngine() { delete impl_; }

} // namespace asspipe
