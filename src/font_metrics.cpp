#include "font_metrics.h"

#include <ft2build.h>
#include FT_FREETYPE_H

#include <cmath>

namespace {

int from_26_6(FT_Pos value) {
    // Round half away from zero so symmetric metrics stay symmetric.
    const double pixels = static_cast<double>(value) / 64.0;
    return static_cast<int>(std::lround(pixels));
}

}  // namespace

FontMetrics::~FontMetrics() {
    if (face_ != nullptr) {
        FT_Done_Face(static_cast<FT_Face>(face_));
    }
    if (library_ != nullptr) {
        FT_Done_FreeType(static_cast<FT_Library>(library_));
    }
}

bool FontMetrics::load(const std::string& path, int pixel_size,
                       int letter_spacing, double line_spacing,
                       std::string& error) {
    if (face_ != nullptr) {
        error = "font is already loaded";
        return false;
    }
    if (pixel_size < 1 || pixel_size > 512) {
        error = "font size must be between 1 and 512 pixels";
        return false;
    }
    if (line_spacing < 0.5 || line_spacing > 8.0) {
        error = "line spacing must be between 0.5 and 8.0";
        return false;
    }

    FT_Library library = nullptr;
    if (FT_Init_FreeType(&library) != 0) {
        error = "cannot initialize FreeType";
        return false;
    }
    FT_Face face = nullptr;
    const FT_Error face_error =
        FT_New_Face(library, path.c_str(), 0, &face);
    if (face_error != 0) {
        FT_Done_FreeType(library);
        error = "cannot open font: " + path;
        return false;
    }
    if (FT_Set_Pixel_Sizes(face, 0, static_cast<FT_UInt>(pixel_size)) != 0) {
        FT_Done_Face(face);
        FT_Done_FreeType(library);
        error = "font does not support size " + std::to_string(pixel_size);
        return false;
    }

    library_ = library;
    face_ = face;
    pixel_size_ = pixel_size;
    letter_spacing_ = letter_spacing;

    const FT_Size_Metrics& size = face->size->metrics;
    ascent_ = from_26_6(size.ascender);
    descent_ = from_26_6(size.descender);  // negative
    if (descent_ > 0) {
        descent_ = -descent_;
    }
    const int natural = ascent_ - descent_;
    line_height_ =
        static_cast<int>(std::lround(static_cast<double>(natural) * line_spacing));
    if (line_height_ < 1) {
        line_height_ = 1;
    }
    return true;
}

bool FontMetrics::glyph(char32_t codepoint, GlyphInfo& out) const {
    const auto cached = cache_.find(codepoint);
    if (cached != cache_.end()) {
        out = cached->second;
        return true;
    }
    if (face_ == nullptr) {
        return false;
    }
    FT_Face face = static_cast<FT_Face>(face_);
    FT_UInt index = FT_Get_Char_Index(face, static_cast<FT_ULong>(codepoint));
    GlyphInfo info;
    info.missing = index == 0;
    if (info.missing) {
        missing_codepoints_[codepoint] = true;
    }
    if (FT_Load_Glyph(face, index, FT_LOAD_DEFAULT) != 0) {
        return false;
    }
    info.advance = from_26_6(face->glyph->metrics.horiAdvance);
    info.bearing_x = from_26_6(face->glyph->metrics.horiBearingX);
    info.bearing_y = from_26_6(face->glyph->metrics.horiBearingY);
    cache_.emplace(codepoint, info);
    out = info;
    return true;
}

int FontMetrics::block_height(int line_count) const {
    if (line_count <= 0) {
        return 0;
    }
    return ascent_ - descent_ + (line_count - 1) * line_height_;
}
