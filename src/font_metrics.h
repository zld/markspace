#pragma once

#include <cstdint>
#include <string>
#include <unordered_map>

struct GlyphInfo {
    int advance = 0;
    int bearing_x = 0;
    int bearing_y = 0;
    bool missing = false;
};

// Owns one FreeType face and caches glyph metrics for a fixed pixel size.
// Missing code points resolve to glyph 0 so callers can measure a fallback
// width; whether that is an error is decided by the caller.
class FontMetrics {
public:
    FontMetrics() = default;
    ~FontMetrics();
    FontMetrics(const FontMetrics&) = delete;
    FontMetrics& operator=(const FontMetrics&) = delete;

    bool load(const std::string& path, int pixel_size, int letter_spacing,
              double line_spacing, std::string& error);
    bool loaded() const { return face_ != nullptr; }

    // Caches lazily; declared const so layout code can hold a const face.
    bool glyph(char32_t codepoint, GlyphInfo& out) const;

    int pixel_size() const { return pixel_size_; }
    int letter_spacing() const { return letter_spacing_; }
    int ascent() const { return ascent_; }
    int descent() const { return descent_; }
    int line_height() const { return line_height_; }
    // Height of a stack of `line_count` lines, including ascent and descent.
    int block_height(int line_count) const;

    // Number of distinct code points whose glyph was missing from the face.
    std::size_t distinct_missing() const { return missing_codepoints_.size(); }

private:
    void* library_ = nullptr;
    void* face_ = nullptr;
    int pixel_size_ = 0;
    int letter_spacing_ = 0;
    int ascent_ = 0;
    int descent_ = 0;
    int line_height_ = 0;
    mutable std::unordered_map<char32_t, GlyphInfo> cache_;
    mutable std::unordered_map<char32_t, bool> missing_codepoints_;
};
