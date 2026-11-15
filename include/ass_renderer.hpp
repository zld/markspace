#pragma once
#include <cstdint>
#include <string>
#include <vector>

namespace assaudit {

struct RenderImageBlock {
  int x = 0, y = 0, w = 0, h = 0;
};

struct RenderResult {
  bool ok = false;
  bool zero_frames = false;
  bool font_fallback = false;
  int region_width = 0;
  int region_height = 0;
  int image_blocks = 0;
  std::string pixel_digest;  // FNV-1a over bitmap payload + block geometry
  std::vector<RenderImageBlock> blocks;
};

// Owns ASS_Library / ASS_Renderer / ASS_Track for one offscreen document.
// No windowing or display calls are ever made.
class AssRenderer {
 public:
  AssRenderer();
  ~AssRenderer();
  AssRenderer(const AssRenderer&) = delete;
  AssRenderer& operator=(const AssRenderer&) = delete;

  // Returns empty string on success, otherwise a human-readable error.
  std::string init(int width, int height);
  std::string renderTrack(const std::string& assText, long long atMs,
                          RenderResult& out);
  int width() const { return width_; }
  int height() const { return height_; }

 private:
  void* library_ = nullptr;   // ASS_Library*
  void* renderer_ = nullptr;  // ASS_Renderer*
  int width_ = 0;
  int height_ = 0;
  bool fallback_seen_ = false;
};

}  // namespace assaudit
