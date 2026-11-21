#include "manifest.h"
#include "unicode.h"
#include "font_metrics.h"
#include "line_breaker.h"

#include <iostream>

int main(int argc, char** argv) {
    if (argc != 2) {
        std::cerr << "usage: subtitle-metrics MANIFEST.json\n";
        return 2;
    }
    Manifest manifest;
    std::vector<ManifestError> errors;
    if (!load_manifest(argv[1], manifest, errors)) {
        for (const ManifestError& error : errors) {
            std::cerr << error.path;
            if (error.item_index >= 0) {
                std::cerr << " (subtitle[" << error.item_index << "]";
                if (!error.item_id.empty()) std::cerr << " " << error.item_id;
                std::cerr << ")";
            }
            std::cerr << ": " << error.message << "\n";
        }
        return 2;
    }
    std::size_t clusters = 0;
    for (const SubtitleItem& item : manifest.items) {
        std::vector<Cluster> shaped;
        std::vector<char32_t> codepoints;
        DecodeError error;
        if (!shape_text(item.text, shaped, error, codepoints)) {
            std::cerr << item.id << ": byte " << error.byte_offset << ": "
                      << error.message << "\n";
            return 3;
        }
        clusters += shaped.size();
    }
    FontMetrics metrics;
    std::string font_error;
    if (!metrics.load(manifest.font.path, manifest.font.size,
                      manifest.font.letter_spacing, manifest.font.line_spacing,
                      font_error)) {
        std::cerr << font_error << "\n";
        return 2;
    }
    const int usable_width =
        manifest.canvas.width - 2 * manifest.canvas.safe_inset;
    int lines = 0;
    int overflows = 0;
    int missing = 0;
    for (const SubtitleItem& item : manifest.items) {
        std::vector<Cluster> shaped;
        std::vector<char32_t> codepoints;
        DecodeError error;
        shape_text(item.text, shaped, error, codepoints);
        ShapedText advances = shape_clusters(shaped, codepoints, metrics);
        TextLayout layout = layout_clusters(shaped, advances, metrics,
                                            usable_width, manifest.max_lines);
        lines += static_cast<int>(layout.lines.size());
        overflows += layout.overflow_count;
        missing += advances.missing_glyphs;
        for (const std::string& message : layout.errors) {
            std::cerr << item.id << ": " << message << "\n";
        }
    }
    std::cout << "subtitles=" << manifest.items.size() << " lines=" << lines
              << " overflows=" << overflows << " missing_glyphs=" << missing
              << "\n";
    return 0;
}
