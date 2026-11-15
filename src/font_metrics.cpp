#include "font_metrics.hpp"

#include <algorithm>
#include <cmath>

namespace sm {

namespace {

constexpr double kFontUnits = 64.0;  // FreeType 26.6 fixed-point divisor

}  // namespace

FontMetrics::~FontMetrics() {
    if (face_ != nullptr) FT_Done_Face(face_);
    if (library_ != nullptr) FT_Done_FreeType(library_);
}

bool FontMetrics::open(const std::string& path, int font_size_px,
                       double scale, Error& error) {
    pixel_size_ = std::max(
        1, static_cast<int>(std::llround(font_size_px * scale)));

    if (FT_Init_FreeType(&library_) != 0) {
        error = {ErrorKind::Font, "failed to initialize FreeType"};
        return false;
    }
    FT_Error ft_err = FT_New_Face(library_, path.c_str(), 0, &face_);
    if (ft_err != 0) {
        error = {ErrorKind::Font,
                 "failed to load font '" + path + "' (code " +
                     std::to_string(ft_err) + ")"};
        face_ = nullptr;  // library_ stays valid; destructor releases it
        return false;
    }
    ft_err = FT_Set_Pixel_Sizes(face_, 0,
                                static_cast<FT_UInt>(pixel_size_));
    if (ft_err != 0) {
        error = {ErrorKind::Font,
                 "font does not support pixel size " +
                     std::to_string(pixel_size_)};
        return false;
    }

    const FT_Size_Metrics& m = face_->size->metrics;
    ascender_ = m.ascender / kFontUnits;
    descender_ = m.descender / kFontUnits;  // typically negative
    const double stored_height = m.height / kFontUnits;
    line_height_ = std::max(stored_height, ascender_ - descender_);
    if (!(line_height_ > 0.0)) line_height_ = pixel_size_ * 1.2;
    return true;
}

bool FontMetrics::isDefaultIgnorable(char32_t cp) {
    if (cp == 0x00AD) return true;                       // soft hyphen
    if (cp >= 0x200B && cp <= 0x200F) return true;       // ZWSP..RLM incl. ZWJ
    if (cp >= 0x202A && cp <= 0x202E) return true;       // bidi embeddings
    if (cp >= 0x2060 && cp <= 0x2064) return true;
    if (cp >= 0x2066 && cp <= 0x2069) return true;
    if (cp >= 0xFE00 && cp <= 0xFE0F) return true;       // variation selectors
    if (cp >= 0xE0100 && cp <= 0xE01EF) return true;
    return false;
}

const FontMetrics::GlyphInfo& FontMetrics::glyphInfo(char32_t cp) {
    auto it = cache_.find(static_cast<uint32_t>(cp));
    if (it != cache_.end()) return it->second;

    GlyphInfo info;
    info.index = FT_Get_Char_Index(face_, static_cast<FT_ULong>(cp));
    if (info.index != 0) {
        FT_Error ft_err =
            FT_Load_Glyph(face_, info.index, FT_LOAD_NO_HINTING);
        if (ft_err == 0) {
            const FT_Glyph_Metrics& gm = face_->glyph->metrics;
            info.present = true;
            info.advance = gm.horiAdvance / kFontUnits;
            info.bearing_x = gm.horiBearingX / kFontUnits;
            info.width = gm.width / kFontUnits;
            info.bearing_y = gm.horiBearingY / kFontUnits;
            info.height = gm.height / kFontUnits;
        }
    }
    auto inserted =
        cache_.emplace(static_cast<uint32_t>(cp), info);
    return inserted.first->second;
}

double FontMetrics::kerning(char32_t left, char32_t right) const {
    if (!FT_HAS_KERNING(face_)) return 0.0;
    FT_UInt li = FT_Get_Char_Index(face_, static_cast<FT_ULong>(left));
    FT_UInt ri = FT_Get_Char_Index(face_, static_cast<FT_ULong>(right));
    if (li == 0 || ri == 0) return 0.0;
    FT_Vector delta{};
    if (FT_Get_Kerning(face_, li, ri, FT_KERNING_DEFAULT, &delta) != 0)
        return 0.0;
    return delta.x / kFontUnits;
}

ClusterMeasurement
FontMetrics::measureCluster(const GraphemeCluster& cluster,
                            std::vector<char32_t>& missing) {
    ClusterMeasurement out;
    double pen = 0.0;
    bool first_ink = true;
    char32_t prev_present_cp = 0;

    for (const Codepoint& cp_node : cluster.cps) {
        const char32_t cp = cp_node.value;
        if (isDefaultIgnorable(cp)) continue;

        const GlyphInfo& g = glyphInfo(cp);
        if (!g.present) {
            missing.push_back(cp);
            prev_present_cp = 0;
            continue;
        }
        if (prev_present_cp != 0) pen += kerning(prev_present_cp, cp);

        const double left = pen + g.bearing_x;
        const double right = pen + g.bearing_x + g.width;
        const double top = g.bearing_y;
        const double bottom = g.bearing_y - g.height;
        if (first_ink) {
            out.left_bearing = left;
            out.right_edge = right;
            out.top_bearing = top;
            out.bottom_edge = bottom;
            first_ink = false;
        } else {
            out.left_bearing = std::min(out.left_bearing, left);
            out.right_edge = std::max(out.right_edge, right);
            out.top_bearing = std::max(out.top_bearing, top);
            out.bottom_edge = std::min(out.bottom_edge, bottom);
        }
        pen += g.advance;
        prev_present_cp = cp;
    }
    out.advance = pen;
    return out;
}

}  // namespace sm
