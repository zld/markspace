#pragma once
//
// renderer.hpp — Offscreen output: PNG encoding and SHA-256 summaries.
// No windowing or GUI dependency; PNG is written with zlib only.
//
#include <cstdint>
#include <string>
#include <vector>

#include "layout_engine.hpp"

namespace asspipe {

// Lower-case hex SHA-256 of the encoded PNG bytes (64 hex chars).
std::string sha256_hex(const std::vector<std::uint8_t>& bytes);

// Encode an RGBA8 buffer as an 8-bit RGBA PNG. Returns false on failure.
bool encode_png(const std::vector<std::uint8_t>& rgba, int width,
                int height, std::vector<std::uint8_t>& out_png);

// Full path of the PNG for event id `n` (zero-padded for stable sorting).
std::string event_png_path(const std::string& output_dir, int n);

} // namespace asspipe
