#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace mp4::fix {

// Deterministic in-memory ISO BMFF fixture builder. Every byte is derived
// from fixed constants so the samples are reproducible across runs.

struct SampleSpec {
    uint32_t video_samples = 2;
    uint32_t audio_samples = 3;
    uint32_t video_duration = 1000;  // track-1 timescale 90000
    uint32_t audio_duration = 1024;  // track-2 timescale 48000
    uint32_t video_size = 40;
    uint32_t audio_size = 12;
};

// Builds an ftyp + moov initialization segment with two tracks (ids 1 and 2),
// trex defaults and minimal stsd sample entries.
std::vector<uint8_t> build_init(const SampleSpec& spec = {});

struct FragmentSpec {
    uint32_t sequence = 1;
    uint64_t video_base_time = 0;
    uint64_t audio_base_time = 0;
    // When true the fragment uses a size=0 mdat (only valid as the last box).
    bool mdat_to_end = false;
    // Alternate box encodings to exercise parser/rewriter paths.
    bool tfdt_64 = false;          // tfdt version 1
    bool trun_v1_negative = false; // trun version 1 with negative ctts offset
    bool explicit_defaults = false;// tfhd carries default duration/size/flags
    bool extended_box_size = false;// moof uses 64-bit largesize
};

// Builds one media segment containing one moof (two trafs) and one mdat.
// Video samples precede audio samples inside the mdat payload.
std::vector<uint8_t> build_fragment(const SampleSpec& sample,
                                    const FragmentSpec& frag);

// Convenience: the six-fragment accepted input set (sequences 1..6).
// tfdt versions, trun versions, explicit defaults and a final size=0 mdat
// alternate across the fragments for coverage.
std::vector<std::pair<std::string, std::vector<uint8_t>>>
build_normal_set(const SampleSpec& sample = {});

// Fault-injection fixture sets. Each returns (file name, bytes).
// "missing"     : normal set with seg-04.m4s removed
// "duplicate"   : two fragments sharing moof sequence 3
// "outoforder"  : file order and moof sequences interleaved (recoverable)
// "length"      : mdat payload shortened so trun sizes do not match
// "drift"       : second init segment style bytes via changed trex defaults
// "encrypted"   : a senc box present, rejected by the parser
// "truncated"   : a box header cut off mid-file
enum class Fault {
    Missing,
    Duplicate,
    OutOfOrder,
    Length,
    Drift,
    Encrypted,
    Truncated
};
std::vector<std::pair<std::string, std::vector<uint8_t>>>
build_fault_set(Fault fault, const SampleSpec& sample = {});

// Writes bytes to path, creating parent directories.
void write_file(const std::string& path,
                const std::vector<uint8_t>& bytes);

}  // namespace mp4::fix
