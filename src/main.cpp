#include "manifest.h"
#include "unicode.h"

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
    std::cout << "subtitles=" << manifest.items.size() << " clusters=" << clusters << "\n";
    return 0;
}
