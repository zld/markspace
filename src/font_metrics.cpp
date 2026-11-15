// FreeType glyph metrics: per-instance library (thread exclusive faces),
// fallback resolution, glyph cache, kerning and 26.6 line metrics.

#include "subtitle/font_metrics.hpp"

#include <ft2build.h>
#include FT_FREETYPE_H
#include FT_GLYPH_H

#include <cmath>

namespace subtitle {

struct FontChain::Impl {
  FT_Library library = nullptr;
  std::vector<FT_Face> faces;

  ~Impl() {
    for (FT_Face f : faces) FT_Done_Face(f);
    if (library) FT_Done_FreeType(library);
  }
};

FontChain::FontChain(const std::vector<std::string>& paths, double size_px)
    : impl_(new Impl) {
  if (paths.empty()) throw FontError("no font configured");
  if (FT_Init_FreeType(&impl_->library) != 0)
    throw FontError("FT_Init_FreeType failed");
  const FT_F26Dot6 size = static_cast<FT_F26Dot6>(std::llround(size_px * 64.0));
  for (const std::string& p : paths) {
    FT_Face face = nullptr;
    FT_Error err = FT_New_Face(impl_->library, p.c_str(), 0, &face);
    if (err != 0 || !face) throw FontError("cannot load font: " + p);
    if ((err = FT_Set_Char_Size(face, 0, size, 72, 72)) != 0) {
      FT_Done_Face(face);
      throw FontError("cannot set size on font: " + p);
    }
    impl_->faces.push_back(face);
    const FT_Size_Metrics& m = face->size->metrics;
    if (m.ascender > ascender_) ascender_ = m.ascender;
    if (m.descender < descender_) descender_ = m.descender;
    if (m.height > line_height_) line_height_ = m.height;
  }
}

FontChain::~FontChain() = default;

const FontChain::GlyphInfo& FontChain::glyph(std::uint32_t cp) {
  auto it = cache_.find(cp);
  if (it != cache_.end()) return it->second;

  GlyphInfo info;
  for (std::size_t fi = 0; fi < impl_->faces.size(); ++fi) {
    FT_UInt g = FT_Get_Char_Index(impl_->faces[fi], cp);
    if (g == 0) continue;
    info.glyph = g;
    info.font_index = static_cast<int>(fi);
    break;
  }
  FT_Face face = impl_->faces.front();
  if (info.font_index < 0) {
    info.missing = true;
    info.glyph = 0;  // .notdef of the primary font
  } else {
    face = impl_->faces[info.font_index];
  }
  if (FT_Load_Glyph(face, info.glyph, FT_LOAD_NO_HINTING) == 0) {
    info.advance = face->glyph->advance.x;
    FT_Glyph g;
    if (FT_Get_Glyph(face->glyph, &g) == 0) {
      FT_BBox box;
      FT_Glyph_Get_CBox(g, FT_GLYPH_BBOX_TRUNCATE, &box);
      info.ink = BBox{box.xMin, box.yMin, box.xMax, box.yMax};
      FT_Done_Glyph(g);
    }
  }
  auto res = cache_.emplace(cp, info);
  return res.first->second;
}

Fixed FontChain::kerning(int font_index, std::uint32_t left,
                         std::uint32_t right) {
  if (font_index < 0 || left == 0 || right == 0) return 0;
  FT_Face face = impl_->faces[font_index];
  if (!FT_HAS_KERNING(face)) return 0;
  FT_Vector delta;
  if (FT_Get_Kerning(face, left, right, FT_KERNING_UNFITTED, &delta) != 0)
    return 0;
  return delta.x;
}

}  // namespace subtitle
