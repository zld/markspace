#pragma once

#include <cstdint>
#include <string>
#include <unordered_map>
#include <vector>

#include <ft2build.h>
#include FT_FREETYPE_H

#include "config.hpp"
#include "utf8.hpp"

namespace sm {

// Metrics of one laid-out grapheme cluster, in font pixel units.
struct ClusterMeasurement {
    double advance = 0.0;        // total pen advance of the cluster
    double left_bearing = 0.0;   // leftmost ink edge relative to pen origin
    double right_edge = 0.0;     // rightmost ink edge relative to pen origin
    double top_bearing = 0.0;    // highest ink edge relative to baseline
    double bottom_edge = 0.0;    // lowest ink edge relative to baseline
};

// Thin FreeType wrapper: one face at one pixel size, with glyph metrics
// cached per codepoint. Missing glyphs are reported, never substituted.
class FontMetrics {
public:
    FontMetrics() = default;
    ~FontMetrics();
    FontMetrics(const FontMetrics&) = delete;
    FontMetrics& operator=(const FontMetrics&) = delete;

    // Loads the font file and selects the pixel size (font size * scale,
    // rounded). Returns false with ErrorKind::Font on any failure.
    bool open(const std::string& path, int font_size_px, double scale,
              Error& error);

    double lineHeight() const { return line_height_; }
    double ascender() const { return ascender_; }
    double descender() const { return descender_; }
    int pixelSize() const { return pixel_size_; }

    // Measure a cluster. Codepoints without a glyph are appended to
    // `missing` (default-ignorable formatting codepoints are exempt).
    ClusterMeasurement measureCluster(const GraphemeCluster& cluster,
                                      std::vector<char32_t>& missing);

    // Horizontal kerning between two base codepoints in pixels; 0 when
    // the face has no kerning table or either glyph is missing.
    double kerning(char32_t left, char32_t right) const;

private:
    struct GlyphInfo {
        FT_UInt index = 0;
        double advance = 0.0;
        double bearing_x = 0.0;
        double width = 0.0;
        double bearing_y = 0.0;
        double height = 0.0;
        bool present = false;
    };

    const GlyphInfo& glyphInfo(char32_t cp);
    static bool isDefaultIgnorable(char32_t cp);

    FT_Library library_ = nullptr;
    FT_Face face_ = nullptr;
    int pixel_size_ = 0;
    double line_height_ = 0.0;
    double ascender_ = 0.0;
    double descender_ = 0.0;
    std::unordered_map<uint32_t, GlyphInfo> cache_;
    GlyphInfo missing_info_{};
};

}  // namespace sm
