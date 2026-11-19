#include <cstdint>
#include <cstdlib>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>

#include "fixture_builder.hpp"

using namespace h264;
using namespace h264::fixture;

namespace {

constexpr unsigned FRAME_NUM_BITS = 4;
constexpr unsigned FIELD_FRAME_NUM_BITS = 6;

// Emits one frame picture (one or more slices).
void emit_frame(std::vector<std::uint8_t>& out, bool idr, unsigned frame_num,
                unsigned slice_type, unsigned pps_id,
                unsigned slices = 1, bool four_byte = true) {
    const unsigned nal_type = idr ? NAL_IDR : NAL_SLICE;
    const unsigned ref_idc = idr ? 3u : (slice_type == SLICE_B ? 0u : 2u);
    const unsigned mb_height = 15;  // 240 / 16
    for (unsigned i = 0; i < slices; ++i) {
        const unsigned first_mb = i * (mb_height * 20 / slices);
        auto rbsp = build_slice_rbsp(first_mb, slice_type, pps_id, frame_num,
                                     FRAME_NUM_BITS, false, false, idr);
        append_nal(out, rbsp, ref_idc, nal_type, four_byte);
    }
}

// Emits a complementary field pair as two field pictures.
void emit_field_pair(std::vector<std::uint8_t>& out, unsigned frame_num) {
    auto top = build_slice_rbsp(0, SLICE_B, 2, frame_num,
                                FIELD_FRAME_NUM_BITS, true, false, false);
    auto bottom = build_slice_rbsp(0, SLICE_B, 2, frame_num,
                                   FIELD_FRAME_NUM_BITS, true, true, false);
    append_nal(out, top, 0, NAL_SLICE, true);
    append_nal(out, bottom, 0, NAL_SLICE, false);
}

// Minimal SEI with one user-data payload so the scanner sees a non-VCL NAL.
void emit_sei(std::vector<std::uint8_t>& out) {
    std::vector<std::uint8_t> rbsp;
    rbsp.push_back(5);  // payload_type = user_data_unregistered
    rbsp.push_back(4);  // payload_size
    const char tag[4] = {'H', '2', '6', '4'};
    for (char c : tag) rbsp.push_back(static_cast<std::uint8_t>(c));
    rbsp.push_back(0x80);  // rbsp trailing bits
    append_nal(out, rbsp, 0, NAL_SEI, false);
}

void emit_filler(std::vector<std::uint8_t>& out, std::size_t bytes) {
    std::vector<std::uint8_t> rbsp(bytes, 0xFF);
    rbsp.push_back(0x80);  // rbsp trailing bits
    append_nal(out, rbsp, 0, NAL_FILLER, false);
}

std::vector<std::uint8_t> build_stream() {
    std::vector<std::uint8_t> out;

    // Three distinct SPS / three PPS to exercise parameter set caching.
    append_nal(out, build_sps_baseline(), 3, NAL_SPS, true);
    append_nal(out, build_sps_high(), 3, NAL_SPS, true);
    append_nal(out, build_sps_interlaced(), 3, NAL_SPS, true);
    append_nal(out, build_pps(0, 0, false), 3, NAL_PPS, true);
    append_nal(out, build_pps(1, 1, true), 3, NAL_PPS, true);
    append_nal(out, build_pps(2, 2, false), 3, NAL_PPS, true);

    // ---- GOP 1: IDR with two slices, then P/B/P, then an interlaced pair.
    append_nal(out, build_aud(), 0, NAL_AUD, false);
    emit_sei(out);
    emit_frame(out, true, 0, SLICE_I, 0, /*slices=*/2);
    emit_frame(out, false, 1, SLICE_P, 0);
    emit_frame(out, false, 2, SLICE_B, 0);
    emit_frame(out, false, 3, SLICE_P, 1);
    emit_field_pair(out, 4);
    emit_filler(out, 768);

    // ---- GOP 2: IDR then P/B/P/B/P (second closed-at-EOF group).
    append_nal(out, build_aud(), 0, NAL_AUD, false);
    emit_frame(out, true, 0, SLICE_I, 0, 1, false);
    emit_frame(out, false, 1, SLICE_P, 0);
    emit_frame(out, false, 2, SLICE_B, 0);
    emit_frame(out, false, 3, SLICE_P, 1);
    emit_frame(out, false, 4, SLICE_B, 0);
    emit_frame(out, false, 5, SLICE_P, 0);
    emit_filler(out, 1024);

    return out;
}

void print_usage() {
    std::cerr << "usage: fixture_gen --output FILE [--corrupt empty-nal]\n";
}

}  // namespace

int main(int argc, char** argv) {
    std::string output;
    std::string corrupt;
    for (int i = 1; i < argc; ++i) {
        const std::string arg = argv[i];
        if (arg == "--output" && i + 1 < argc) {
            output = argv[++i];
        } else if (arg.rfind("--output=", 0) == 0) {
            output = arg.substr(9);
        } else if (arg == "--corrupt" && i + 1 < argc) {
            corrupt = argv[++i];
        } else {
            print_usage();
            return 64;
        }
    }
    if (output.empty()) {
        print_usage();
        return 64;
    }

    std::vector<std::uint8_t> stream = build_stream();
    if (corrupt == "empty-nal") {
        // Two adjacent start codes form a zero-length NAL: deterministic
        // structural error for strict-mode and diagnostic tests.
        stream.insert(stream.begin(), {0x00, 0x00, 0x01});
        stream.insert(stream.begin() + 3, {0x00, 0x00, 0x01});
    } else if (!corrupt.empty()) {
        std::cerr << "unknown --corrupt mode: " << corrupt << "\n";
        return 64;
    }

    std::ofstream file(output, std::ios::binary | std::ios::trunc);
    if (!file) {
        std::cerr << "cannot open output: " << output << "\n";
        return 70;
    }
    file.write(reinterpret_cast<const char*>(stream.data()),
               static_cast<std::streamsize>(stream.size()));
    if (!file) {
        std::cerr << "write failure: " << output << "\n";
        return 70;
    }
    std::cout << "wrote " << stream.size() << " bytes to " << output << "\n";
    return 0;
}
