#pragma once

// FreeType-backed font chain: per-thread glyph metrics, fallback and cache.

#include <memory>
#include <string>
#include <unordered_map>
#include <vector>

#include "subtitle/model.hpp"

namespace subtitle {

class FontChain {
 public:
  // Loads every font at the given pixel size; first font is primary.
  // Throws FontError if any font fails to load.
  FontChain(const std::vector<std::string>& paths, double size_px);
  ~FontChain();

  FontChain(const FontChain&) = delete;
  FontChain& operator=(const FontChain&) = delete;

  struct GlyphInfo {
    std::uint32_t glyph = 0;   // glyph id inside font_index
    int font_index = -1;       // -1 when missing everywhere
    bool missing = false;
    Fixed advance = 0;         // 26.6 horizontal advance
    BBox ink;                  // 26.6 ink box relative to pen origin
  };

  // Resolve and measure one codepoint, walking the fallback chain.
  // Results are cached. Missing glyphs report the primary font's .notdef
  // advance so layout stays stable.
  const GlyphInfo& glyph(std::uint32_t cp);

  // Kerning adjustment between two glyphs of the same font (0 otherwise).
  Fixed kerning(int font_index, std::uint32_t left, std::uint32_t right);

  Fixed line_height() const { return line_height_; }
  Fixed ascender() const { return ascender_; }
  Fixed descender() const { return descender_; }

 private:
  struct Impl;
  std::unique_ptr<Impl> impl_;
  std::unordered_map<std::uint32_t, GlyphInfo> cache_;
  Fixed line_height_ = 0, ascender_ = 0, descender_ = 0;
};

}  // namespace subtitle
