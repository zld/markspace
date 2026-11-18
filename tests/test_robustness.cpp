// Tests for the robustness module.
#include <cstdio>
#include <string>
#include <vector>

#include "access_unit.hpp"
#include "byte_source.hpp"
#include "sample_generator.hpp"
#include "test_util.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

AnalysisResult analyze_bytes(const std::vector<std::uint8_t>& bytes) {
    const std::string path = selftest::write_temp("h264inspect_stream.bin",
                                        std::string(bytes.begin(), bytes.end()));
    ByteSource source;
    Diagnostic err;
    CHECK(source.open(path, ByteSource::kDefaultMaxSize, &err));
    AnalysisResult result = analyze_stream(source);
    std::remove(path.c_str());
    return result;
}

void test_mutated_streams_never_crash() {
    // Deterministic mutation sweep: corrupt bytes, truncate, and splice in
    // start codes. The analyzer must always return a bounded, self-consistent
    // result rather than crash or report offsets past the input.
    const std::vector<std::uint8_t> base = generate_reference_sample();
    std::uint32_t seed = 0x12345678u;
    auto next = [&seed]() {
        seed = seed * 1664525u + 1013904223u;
        return seed;
    };
    for (int iteration = 0; iteration < 300; ++iteration) {
        std::vector<std::uint8_t> bytes = base;
        const int mode = iteration % 3;
        if (mode == 0) {
            const std::size_t edits = 1 + next() % 4;
            for (std::size_t i = 0; i < edits; ++i) {
                bytes[next() % bytes.size()] = static_cast<std::uint8_t>(next());
            }
        } else if (mode == 1) {
            bytes.resize(next() % (bytes.size() + 1));
        } else {
            const std::size_t at = next() % (bytes.size() + 1);
            const std::vector<std::uint8_t> code = {0x00, 0x00, 0x00, 0x01, 0x65};
            bytes.insert(bytes.begin() + static_cast<std::ptrdiff_t>(at), code.begin(),
                         code.end());
        }
        const AnalysisResult result = analyze_bytes(bytes);
        for (const Diagnostic& d : result.errors) {
            CHECK(d.offset <= bytes.size());
            CHECK(!d.message.empty());
        }
        for (const FrameInfo& frame : result.frames) {
            CHECK(frame.first_nal_offset <= bytes.size());
            CHECK(frame.slice_count > 0);
        }
        for (const GopGroup& group : result.groups) {
            CHECK(group.start_nal_offset <= bytes.size());
        }
        std::uint32_t frames_in_groups = 0;
        for (const GopGroup& group : result.groups) {
            frames_in_groups += group.frame_count;
        }
        CHECK_EQ(frames_in_groups, static_cast<std::uint32_t>(result.frames.size()));
    }
}

}  // namespace

REGISTER_TEST(test_mutated_streams_never_crash);
