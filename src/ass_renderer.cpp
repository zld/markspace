#include "ass_renderer.hpp"

#include <ass/ass.h>

#include <algorithm>
#include <cstring>

#include "util.hpp"

namespace assaudit {
namespace {

void messageCallback(int level, const char* fmt, va_list args, void* ctx) {
  char buf[1024];
  vsnprintf(buf, sizeof(buf), fmt, args);
  std::string msg = buf;
  std::string low = toLower(msg);
  bool* fallback = static_cast<bool*>(ctx);
  if (low.find("not found") != std::string::npos ||
      low.find("fallback") != std::string::npos ||
      low.find("fail to find") != std::string::npos)
    *fallback = true;
  (void)level;
}

}  // namespace

AssRenderer::AssRenderer() = default;

AssRenderer::~AssRenderer() {
  if (renderer_) ass_renderer_done(static_cast<ASS_Renderer*>(renderer_));
  if (library_) ass_library_done(static_cast<ASS_Library*>(library_));
}

std::string AssRenderer::init(int width, int height) {
  if (width <= 0 || height <= 0 || width > 16384 || height > 16384)
    return "invalid render resolution " + std::to_string(width) + "x" +
           std::to_string(height);
  width_ = width;
  height_ = height;
  ASS_Library* lib = ass_library_init();
  if (!lib) return "ass_library_init failed";
  ass_set_message_cb(lib, messageCallback, &fallback_seen_);
  library_ = lib;
  ASS_Renderer* r = ass_renderer_init(lib);
  if (!r) return "ass_renderer_init failed";
  renderer_ = r;
  ass_set_frame_size(r, width, height);
  ass_set_storage_size(r, width, height);
  ass_set_pixel_aspect(r, 1.0);
  ass_set_fonts(r, nullptr, nullptr, ASS_FONTPROVIDER_AUTODETECT, nullptr, 1);
  return "";
}

std::string AssRenderer::renderTrack(const std::string& assText,
                                     long long atMs, RenderResult& out) {
  fallback_seen_ = false;
  ASS_Library* lib = static_cast<ASS_Library*>(library_);
  ASS_Track* track = ass_read_memory(
      lib, const_cast<char*>(assText.data()), assText.size(), nullptr);
  if (!track) return "ass_read_memory rejected the synthesized document";
  ASS_Renderer* r = static_cast<ASS_Renderer*>(renderer_);
  ASS_Image* img = ass_render_frame(r, track, atMs, nullptr);
  out = RenderResult{};
  out.font_fallback = fallback_seen_;
  uint64_t digest = 1469598103934665603ULL;
  int minX = width_, minY = height_, maxX = -1, maxY = -1;
  for (ASS_Image* p = img; p; p = p->next) {
    RenderImageBlock blk;
    blk.x = p->dst_x;
    blk.y = p->dst_y;
    blk.w = p->w;
    blk.h = p->h;
    out.blocks.push_back(blk);
    ++out.image_blocks;
    minX = std::min(minX, p->dst_x);
    minY = std::min(minY, p->dst_y);
    maxX = std::max(maxX, p->dst_x + p->w);
    maxY = std::max(maxY, p->dst_y + p->h);
    // Geometry and color are part of the digest; bitmap payload follows.
    uint32_t geom[4] = {static_cast<uint32_t>(p->dst_x),
                        static_cast<uint32_t>(p->dst_y),
                        static_cast<uint32_t>(p->w),
                        static_cast<uint32_t>(p->h)};
    digest = fnv1a64(reinterpret_cast<uint8_t*>(geom), sizeof(geom), digest);
    uint32_t color = p->color;
    digest = fnv1a64(reinterpret_cast<uint8_t*>(&color), sizeof(color),
                     digest);
    size_t payload = static_cast<size_t>(p->w) * static_cast<size_t>(p->h);
    digest = fnv1a64(p->bitmap, payload, digest);
  }
  out.pixel_digest = hex64(digest);
  if (img) {
    out.ok = true;
    out.region_width = maxX - minX;
    out.region_height = maxY - minY;
  } else {
    out.zero_frames = true;
  }
  ass_free_track(track);
  return "";
}

}  // namespace assaudit
