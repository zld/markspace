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
    for (const SubtitleItem& item : manifest.items) {
        std::vector<CodePoint> decoded;
        DecodeError error;
        if (!decode_utf8(item.text, decoded, error)) {
            std::cerr << item.id << ": byte " << error.byte_offset << ": "
                      << error.message << "\n";
            return 3;
        }
    }
    std::cout << "subtitles=" << manifest.items.size() << "\n";
    return 0;
}
