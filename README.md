# subtitle-metrics

Offline multilingual subtitle layout metrics tool. Given a JSON subtitle
file and a local TTF/OTF font, it measures each subtitle's bounding box,
line break positions and safe-area overflows without rendering any image.

- C++17, CMake 3.20+, FreeType 2.13+, nlohmann/json 3.11+
- Self-contained UTF-8 decoder, grapheme cluster segmentation and line breaker
- Pure terminal tool: no windows, no browser, no network access

## Dependencies

### macOS (Homebrew)

```sh
brew install cmake freetype nlohmann-json
```

### Debian / Ubuntu

```sh
sudo apt-get install build-essential cmake libfreetype6-dev nlohmann-json3-dev
```

## Build

```sh
cmake -S . -B build
cmake --build build -j
```

(Module documentation and usage examples are added with later commits.)
