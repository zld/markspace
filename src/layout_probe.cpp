#include "layout_probe.hpp"

#include "inline_tags.hpp"

#include <ass/ass.h>

#include <algorithm>
#include <array>
#include <cstdarg>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <memory>
#include <mutex>
#include <string>

namespace sp {

namespace {

// ---------------------------------------------------------------- SHA-256
constexpr uint32_t kShaK[64] = {
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2};

inline uint32_t rotr(uint32_t x, int n) { return (x >> n) | (x << (32 - n)); }

void sha256_compress(uint32_t state[8], const unsigned char block[64]) {
    uint32_t w[64];
    for (int i = 0; i < 16; ++i) {
        w[i] = (uint32_t(block[i * 4]) << 24) | (uint32_t(block[i * 4 + 1]) << 16) |
               (uint32_t(block[i * 4 + 2]) << 8) | uint32_t(block[i * 4 + 3]);
    }
    for (int i = 16; i < 64; ++i) {
        uint32_t s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >> 3);
        uint32_t s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >> 10);
        w[i] = w[i - 16] + s0 + w[i - 7] + s1;
    }
    uint32_t a = state[0], b = state[1], c = state[2], d = state[3];
    uint32_t e = state[4], f = state[5], g = state[6], h = state[7];
    for (int i = 0; i < 64; ++i) {
        uint32_t S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
        uint32_t ch = (e & f) ^ (~e & g);
        uint32_t t1 = h + S1 + ch + kShaK[i] + w[i];
        uint32_t S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
        uint32_t maj = (a & b) ^ (a & c) ^ (b & c);
        uint32_t t2 = S0 + maj;
        h = g; g = f; f = e; e = d + t1;
        d = c; c = b; b = a; a = t1 + t2;
    }
    state[0] += a; state[1] += b; state[2] += c; state[3] += d;
    state[4] += e; state[5] += f; state[6] += g; state[7] += h;
}

std::string sha256_of(const std::vector<unsigned char>& data) {
    uint32_t state[8] = {0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
                         0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19};
    size_t total = data.size();
    size_t padded = ((total + 8) / 64 + 1) * 64;
    std::vector<unsigned char> buffer(padded, 0);
    if (total) std::memcpy(buffer.data(), data.data(), total);
    buffer[total] = 0x80;
    uint64_t bits = static_cast<uint64_t>(total) * 8;
    for (int i = 0; i < 8; ++i)
        buffer[padded - 1 - i] = static_cast<unsigned char>((bits >> (8 * i)) & 0xFF);
    for (size_t off = 0; off < padded; off += 64) sha256_compress(state, &buffer[off]);
    static const char* hex = "0123456789abcdef";
    std::string out;
    out.reserve(64);
    for (uint32_t word : state) {
        for (int shift = 28; shift >= 0; shift -= 4) out += hex[(word >> shift) & 0xF];
    }
    return out;
}

std::mutex g_lib_mutex;

std::string sanitize_field(const std::string& text, bool strip_commas) {
    std::string out;
    for (unsigned char ch : text) {
        if (ch == '\r' || ch == '\n' || ch < 0x20) continue;
        if (ch == '{' || ch == '}' || ch == '\\') continue;
        if (strip_commas && ch == ',') { out += ' '; continue; }
        out += static_cast<char>(ch);
    }
    return out;
}

std::string fmt_colour(int value) {
    char buf[16];
    std::snprintf(buf, sizeof buf, "&H%08X", static_cast<unsigned>(value));
    return buf;
}

std::string fmt_num(double v) {
    char buf[64];
    std::snprintf(buf, sizeof buf, "%.4f", v);
    std::string s(buf);
    // Trim trailing zeros but keep a valid decimal literal.
    size_t dot = s.find('.');
    if (dot != std::string::npos) {
        size_t last = s.find_last_not_of('0');
        if (last != std::string::npos && last > dot) s.erase(last + 1);
        else if (last == dot) s.erase(dot);
        if (s.empty()) s = "0";
    }
    return s;
}

std::string style_line(const StyleDefinition& record) {
    const EffStyle& s = record.style;
    const std::string font = sanitize_field(s.font_name.cleared ? std::string() : s.font_name.value, true);
    std::string out = "Style: " + sanitize_field(record.name, true) + "," + (font.empty() ? "Arial" : font) +
                      "," + fmt_num(s.font_size.value) + "," + fmt_colour(s.primary_colour.value) + "," +
                      fmt_colour(s.secondary_colour.value) + "," + fmt_colour(s.outline_colour.value) + "," +
                      fmt_colour(s.back_colour.value) + "," + std::to_string(s.bold.value ? 1 : 0) + "," +
                      std::to_string(s.italic.value ? 1 : 0) + "," + std::to_string(s.underline.value ? 1 : 0) +
                      "," + std::to_string(s.strikeout.value ? 1 : 0) + "," + fmt_num(s.scale_x.value) + "," +
                      fmt_num(s.scale_y.value) + "," + fmt_num(s.spacing.value) + "," + fmt_num(s.angle.value) +
                      "," + std::to_string(static_cast<int>(s.border_style.value)) + "," + fmt_num(s.outline.value) +
                      "," + fmt_num(s.shadow.value) + "," + std::to_string(s.alignment.value) + "," +
                      std::to_string(s.margin_l.value) + "," + std::to_string(s.margin_r.value) + "," +
                      std::to_string(s.margin_v.value) + "," + std::to_string(s.encoding.value);
    return out;
}

const char* kStyleFormat =
    "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, "
    "Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, "
    "Shadow, Alignment, MarginL, MarginR, MarginV, Encoding";

std::string build_script(const std::string& style_name, const std::string& name_field,
                         const std::string& effect_field, const std::string& text,
                         const std::vector<StyleDefinition>& styles, const RenderOptions& options,
                         const ScriptInfo& info) {
    std::string script;
    script += "[Script Info]\r\n";
    // Honour the script's own resolution (libass scales it to the frame). If
    // the script omits a resolution, fall back to the canvas so positions map
    // one-to-one to pixels.
    const int play_res_x = info.play_res_x > 0 ? info.play_res_x : options.canvas_width;
    const int play_res_y = info.play_res_y > 0 ? info.play_res_y : options.canvas_height;
    script += "ScriptType: v4.00+\r\n";
    script += "PlayResX: " + std::to_string(play_res_x) + "\r\n";
    script += "PlayResY: " + std::to_string(play_res_y) + "\r\n";
    script += "WrapStyle: " + std::to_string(info.wrap_style) + "\r\n";
    script += std::string("ScaledBorderAndShadow: ") + (info.scaled_border_and_shadow ? "yes" : "no") + "\r\n";
    script += "YCbCr Matrix: None\r\n\r\n";
    script += "[V4+ Styles]\r\n";
    script += kStyleFormat;
    script += "\r\n";
    for (const StyleDefinition& record : styles) script += style_line(record) + "\r\n";
    script += "\r\n[Events]\r\n";
    script += "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n";
    script += "Dialogue: 0,0:00:00.00,0:00:10.00," + sanitize_field(style_name, true) + "," +
              sanitize_field(name_field, true) + ",0,0,0," + sanitize_field(effect_field, true) + "," + text +
              "\r\n\r\n";
    return script;
}

struct MessageSink {
    std::vector<std::string> messages;
    bool active = false;
};

void message_callback(int level, const char* fmt, va_list args, void* data) {
    MessageSink* sink = static_cast<MessageSink*>(data);
    if (!sink || !sink->active || level > 3) return;
    char buf[1024];
    std::vsnprintf(buf, sizeof buf, fmt, args);
    std::string text = sanitize_for_output(buf, 500);
    if (!text.empty() && text.back() == '\n') text.pop_back();
    sink->messages.push_back(std::move(text));
}

}  // namespace

std::string sha256_hex(const unsigned char* data, size_t size) {
    std::vector<unsigned char> copy(data, data + size);
    return sha256_of(copy);
}

struct LayoutProbe::Impl {
    ASS_Library* library = nullptr;
    ASS_Renderer* renderer = nullptr;
    RenderOptions options;
    ScriptInfo info;
    std::vector<StyleDefinition> styles;
    MessageSink sink;
    bool ready = false;
};

LayoutProbe::LayoutProbe() : impl_(new Impl) {}
LayoutProbe::~LayoutProbe() {
    if (impl_->renderer) ass_renderer_done(impl_->renderer);
    if (impl_->library) ass_library_done(impl_->library);
    delete impl_;
}

bool LayoutProbe::initialize(const RenderOptions& options, std::string& error) {
    impl_->options = options;
    impl_->library = ass_library_init();
    if (!impl_->library) { error = "libass: failed to initialize library"; return false; }
    if (!options.font_dir.empty()) ass_set_fonts_dir(impl_->library, options.font_dir.c_str());
    ass_set_message_cb(impl_->library, message_callback, &impl_->sink);
    impl_->renderer = ass_renderer_init(impl_->library);
    if (!impl_->renderer) { error = "libass: failed to initialize renderer"; return false; }
    ass_set_frame_size(impl_->renderer, options.canvas_width, options.canvas_height);
    ass_set_storage_size(impl_->renderer, options.canvas_width, options.canvas_height);
    const char* default_family = options.default_family.empty() ? nullptr : options.default_family.c_str();
    int provider = options.font_dir.empty() ? ASS_FONTPROVIDER_AUTODETECT : ASS_FONTPROVIDER_NONE;
    ass_set_fonts(impl_->renderer, nullptr, default_family, provider, nullptr, 0);
    impl_->ready = true;
    return true;
}

void LayoutProbe::set_context(const ScriptInfo& info, const std::vector<StyleDefinition>& styles) {
    impl_->info = info;
    impl_->styles = styles;
}

RenderResult LayoutProbe::render_event(const Event& event, const EffStyle& style,
                                       const std::vector<ChainLink>& chain) {
    RenderResult result;
    if (!impl_->ready) { result.error = "renderer not initialized"; return result; }

    EffStyle effective = style;

    std::vector<StyleDefinition> styles = impl_->styles;
    // Ensure \r references resolve to the same fully-merged values the report
    // shows. Event-specific margins live on the event style only.
    bool replaced = false;
    for (StyleDefinition& record : styles) {
        if (record.name == event.style_name) {
            record.style = effective;
            replaced = true;
            break;
        }
    }
    const std::string render_style_name = replaced ? event.style_name : "__sp_event_style__";
    if (!replaced) styles.push_back(StyleDefinition{render_style_name, effective});

    std::string text = canonicalize_for_render(event.text);
    std::string script = build_script(render_style_name, event.name, event.effect, text, styles,
                                      impl_->options, impl_->info);

    ASS_Track* track = ass_new_track(impl_->library);
    if (!track) { result.error = "libass: failed to allocate track"; return result; }
    ass_process_data(track, script.data(), static_cast<int>(script.size()));

    impl_->sink.messages.clear();
    impl_->sink.active = true;
    int detect_change = 0;
    ASS_Image* images = ass_render_frame(impl_->renderer, track, 1000, &detect_change);
    impl_->sink.active = false;

    bool font_failure = false;
    bool font_fallback = false;
    auto analyse_messages = [&](const std::vector<std::string>& messages) {
        for (const std::string& msg : messages) {
            bool fontselect = msg.find("fontselect") != std::string::npos;
            if (fontselect && msg.find("failed to find any fallback") != std::string::npos) {
                font_failure = true;
                if (std::find(result.font_warnings.begin(), result.font_warnings.end(), msg) ==
                    result.font_warnings.end())
                    result.font_warnings.push_back(msg);
            } else if (fontselect && msg.find("Using default font family") != std::string::npos) {
                font_fallback = true;
                if (std::find(result.font_warnings.begin(), result.font_warnings.end(), msg) ==
                    result.font_warnings.end())
                    result.font_warnings.push_back(msg);
            } else if (msg.find("no style named") != std::string::npos) {
                // libass prefixes its messages with a per-track heap address;
                // strip it so repeated runs produce byte-identical output.
                size_t close = msg.find("]: ");
                std::string cleaned = close != std::string::npos ? msg.substr(close + 3) : msg;
                if (std::find(result.font_warnings.begin(), result.font_warnings.end(), cleaned) ==
                    result.font_warnings.end())
                    result.font_warnings.push_back(cleaned);
            }
        }
    };
    analyse_messages(impl_->sink.messages);

    const int W = impl_->options.canvas_width;
    const int H = impl_->options.canvas_height;
    struct Rect { int x0, y0, x1, y1; };
    std::vector<Rect> rects;
    bool clipped = false;
    for (ASS_Image* img = images; img; img = img->next) {
        if (img->w <= 0 || img->h <= 0) continue;
        // libass crops bitmaps at the frame edge; a tile that ends exactly on
        // an edge is therefore either clipped or flush with it.
        if (img->dst_x <= 0 || img->dst_y <= 0 || img->dst_x + img->w >= W ||
            img->dst_y + img->h >= H)
            clipped = true;
        int x0 = std::max(0, img->dst_x);
        int y0 = std::max(0, img->dst_y);
        int x1 = std::min(W, img->dst_x + img->w);
        int y1 = std::min(H, img->dst_y + img->h);
        if (x1 > x0 && y1 > y0) rects.push_back(Rect{x0, y0, x1, y1});
    }

    std::string stripped;
    {
        // Visible text check: strip override blocks and escape sequences.
        bool in_block = false;
        for (size_t i = 0; i < text.size(); ++i) {
            char ch = text[i];
            if (ch == '{') { in_block = true; continue; }
            if (ch == '}' && in_block) { in_block = false; continue; }
            if (in_block) continue;
            if (ch == '\\' && i + 1 < text.size() &&
                (text[i + 1] == 'N' || text[i + 1] == 'n' || text[i + 1] == 'h')) {
                ++i;
                continue;
            }
            stripped += ch;
        }
        stripped = trim_ascii(stripped);
    }
    result.empty_text = stripped.empty() && images == nullptr;

    if (rects.empty()) {
        result.sha256 = sha256_of(std::vector<unsigned char>());
        result.ok = !font_failure;
        if (font_failure) {
            result.error = "font fallback failed";
        } else if (stripped.empty()) {
            result.empty_text = true;
        } else {
            result.fully_transparent = true;
        }
        ass_free_track(track);
        return result;
    }

    int x0 = W, y0 = H, x1 = 0, y1 = 0;
    for (const Rect& r : rects) {
        x0 = std::min(x0, r.x0); y0 = std::min(y0, r.y0);
        x1 = std::max(x1, r.x1); y1 = std::max(y1, r.y1);
    }
    const int crop_w = x1 - x0;
    const int crop_h = y1 - y0;
    std::vector<unsigned char> canvas(static_cast<size_t>(crop_w) * crop_h * 4, 0);

    long long nontransparent = 0;
    for (ASS_Image* img = images; img; img = img->next) {
        if (img->w <= 0 || img->h <= 0 || !img->bitmap) continue;
        const int src_a = 255 - static_cast<int>(img->color & 0xFF);
        const unsigned char sr = static_cast<unsigned char>((img->color >> 24) & 0xFF);
        const unsigned char sg = static_cast<unsigned char>((img->color >> 16) & 0xFF);
        const unsigned char sb = static_cast<unsigned char>((img->color >> 8) & 0xFF);
        for (int row = 0; row < img->h; ++row) {
            int dy = img->dst_y + row;
            if (dy < y0 || dy >= y1) continue;
            const unsigned char* line = img->bitmap + static_cast<size_t>(row) * img->stride;
            for (int col = 0; col < img->w; ++col) {
                int dx = img->dst_x + col;
                if (dx < x0 || dx >= x1) continue;
                int alpha = line[col] * src_a / 255;
                if (alpha <= 0) continue;
                unsigned char* px = &canvas[(static_cast<size_t>(dy - y0) * crop_w + (dx - x0)) * 4];
                int dst_a = px[3];
                int out_a = alpha + dst_a * (255 - alpha) / 255;
                if (out_a <= 0) continue;
                auto blend = [&](unsigned char src_c, unsigned char dst_c) {
                    return static_cast<unsigned char>(
                        (src_c * alpha + dst_c * dst_a * (255 - alpha) / 255) / out_a);
                };
                px[0] = blend(sr, px[0]);
                px[1] = blend(sg, px[1]);
                px[2] = blend(sb, px[2]);
                px[3] = static_cast<unsigned char>(out_a);
            }
        }
    }
    for (size_t i = 3; i < canvas.size(); i += 4) {
        if (canvas[i] > 0) ++nontransparent;
    }

    result.has_area = true;
    result.x = x0;
    result.y = y0;
    result.width = crop_w;
    result.height = crop_h;
    result.visible_width = crop_w;
    result.visible_height = crop_h;
    result.nontransparent_pixels = nontransparent;
    result.sha256 = sha256_of(canvas);
    result.clipped = clipped;
    result.ok = !font_failure;
    if (font_failure) result.error = "font fallback failed";
    if (nontransparent == 0) {
        result.fully_transparent = true;
        result.has_area = false;
        result.width = 0;
        result.height = 0;
        result.x = 0;
        result.y = 0;
        result.visible_width = 0;
        result.visible_height = 0;
    }
    (void)font_fallback;
    (void)chain;
    ass_free_track(track);
    return result;
}

}  // namespace sp
