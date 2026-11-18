#pragma once

#include "box_reader.hpp"
#include "fragment_index.hpp"

#include <cstdint>
#include <cstring>
#include <string>
#include <vector>

namespace mp4 {

// Builds a FourCC from a 4-char string literal without a trailing NUL.
inline FourCC mk4(const char* s) {
    FourCC t;
    std::memcpy(t.v, s, 4);
    return t;
}

// Internal box-writing helpers shared by the rewriter translation units.

void put_u32(std::vector<uint8_t>& out, uint32_t v);
void put_u64(std::vector<uint8_t>& out, uint64_t v);
void put_fourcc(std::vector<uint8_t>& out, const FourCC& t);
void put_fullbox_header(std::vector<uint8_t>& out, const FourCC& t,
                        uint8_t version, uint32_t flags);
// Patches the size field of the box whose header starts at `at`.
void patch_box_size(std::vector<uint8_t>& out, size_t at);

void write_mfhd_box(std::vector<uint8_t>& out, uint32_t sequence);
void write_tfhd_box(std::vector<uint8_t>& out, const TrackFragment& tf,
                    const std::vector<uint8_t>& src, uint64_t new_base);
void write_tfdt_box(std::vector<uint8_t>& out, uint64_t dts);
void write_trun_box(std::vector<uint8_t>& out, const Box& trun,
                    const std::vector<uint8_t>& src, int32_t offset_shift);

uint64_t fragment_duration(const TrackFragment& tf);
std::string pad_sequence(uint32_t sequence);

}  // namespace mp4
