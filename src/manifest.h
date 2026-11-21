#pragma once

#include <cstddef>
#include <string>
#include <vector>

struct ManifestFont {
    std::string path;
    int size = 0;
    int letter_spacing = 0;
    double line_spacing = 0.0;
    std::string missing_glyph;
};

struct ManifestCanvas {
    int width = 0;
    int safe_inset = 0;
};

struct SubtitleItem {
    std::string id;
    std::string language;
    std::string text;
};

struct ManifestError {
    std::string path;
    std::string message;
    int item_index = -1;
    std::string item_id;
};

struct Manifest {
    ManifestFont font;
    ManifestCanvas canvas;
    int max_lines = 0;
    std::vector<SubtitleItem> items;
};

// Reads and validates a UTF-8 JSON manifest. Every validation problem is
// appended to `errors`; the return value is false when the manifest cannot be
// used at all.
bool load_manifest(const std::string& path, Manifest& manifest,
                   std::vector<ManifestError>& errors);
