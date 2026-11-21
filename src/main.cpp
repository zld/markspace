#include "manifest.h"

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
            std::cerr << error.path << ": " << error.message << "\n";
        }
        return 2;
    }
    std::cout << "subtitles=" << manifest.items.size() << "\n";
    return 0;
}
