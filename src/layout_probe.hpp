#pragma once

#include <string>
#include <vector>

#include "ass_document.hpp"
#include "style_resolver.hpp"

namespace sp {

struct RenderOptions {
    int canvas_width = 1920;
    int canvas_height = 1080;
    std::string font_dir;        // scanned directly; libass font provider = NONE
    std::string default_family;  // fallback family passed to libass
};

struct StyleDefinition {
    std::string name;
    EffStyle style;
};

struct RenderResult {
    bool ok = false;
    bool empty_text = false;
    bool fully_transparent = false;
    bool clipped = false;
    bool has_area = false;
    int x = 0;
    int y = 0;
    int width = 0;
    int height = 0;
    int visible_width = 0;
    int visible_height = 0;
    long long nontransparent_pixels = 0;
    std::string sha256;  // of the composited RGBA canvas crop
    std::vector<std::string> font_warnings;
    std::string error;
};

// Off-screen renderer. Not thread-safe; give each worker its own instance.
class LayoutProbe {
  public:
    LayoutProbe();
    ~LayoutProbe();
    LayoutProbe(const LayoutProbe&) = delete;
    LayoutProbe& operator=(const LayoutProbe&) = delete;

    bool initialize(const RenderOptions& options, std::string& error);
    void set_context(const ScriptInfo& info, const std::vector<StyleDefinition>& styles);
    RenderResult render_event(const Event& event, const EffStyle& style,
                              const std::vector<ChainLink>& chain);

  private:
    struct Impl;
    Impl* impl_ = nullptr;
};

// Dependency-free SHA-256 hex digest helper.
std::string sha256_hex(const unsigned char* data, size_t size);
}  // namespace sp
