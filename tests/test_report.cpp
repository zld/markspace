// Tests for the report module.
#include <cstdio>
#include <string>
#include <vector>

#include "access_unit.hpp"
#include "report.hpp"
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

void test_generated_sample_matches_reference() {
    const AnalysisResult result = analyze_bytes(generate_reference_sample());
    CHECK(result.errors.empty());
    const SummaryCounts counts = summarize(result);
    CHECK_EQ(counts.i_frames, std::uint32_t{1});
    CHECK_EQ(counts.p_frames, std::uint32_t{1});
    CHECK_EQ(counts.b_frames, std::uint32_t{1});
    CHECK_EQ(counts.slice_count, std::uint32_t{3});
    CHECK_EQ(counts.group_count, std::uint32_t{1});
    CHECK_EQ(result.sps_list[0].width, std::uint32_t{320});
    CHECK_EQ(result.sps_list[0].height, std::uint32_t{192});
    CHECK(!result.sps_list[0].interlaced);
    // The group must start at the first byte of the stream.
    CHECK_EQ(result.groups[0].start_nal_offset, std::uint64_t{0});
}

void test_interlaced_sample_reports_fields() {
    const AnalysisResult result = analyze_bytes(generate_interlaced_sample());
    CHECK(result.errors.empty());
    CHECK(result.sps_list[0].interlaced);
    const SummaryCounts counts = summarize(result);
    CHECK_EQ(counts.i_fields + counts.p_fields, std::uint32_t{4});
    CHECK_EQ(counts.field_pairs, std::uint32_t{2});
    CHECK_EQ(counts.isolated_fields, std::uint32_t{0});
    CHECK_EQ(counts.i_frames, std::uint32_t{1});
    CHECK_EQ(counts.p_frames, std::uint32_t{1});
    CHECK_EQ(result.frames.size(), std::size_t{2});
}

void test_json_report_has_required_sections() {
    const AnalysisResult result = analyze_bytes(generate_reference_sample());
    const std::string json = render_report(result, ReportFormat::Json);
    for (const char* key : {"\"sps\"", "\"frames\"", "\"groups\"",
                            "\"exp_golomb\"", "\"errors\""}) {
        CHECK(json.find(key) != std::string::npos);
    }
    // Balanced braces indicate structurally valid JSON.
    int depth = 0;
    bool balanced = true;
    bool in_string = false;
    bool escaped = false;
    for (char c : json) {
        if (in_string) {
            if (escaped) {
                escaped = false;
            } else if (c == '\\') {
                escaped = true;
            } else if (c == '"') {
                in_string = false;
            }
            continue;
        }
        if (c == '"') {
            in_string = true;
        } else if (c == '{' || c == '[') {
            ++depth;
        } else if (c == '}' || c == ']') {
            --depth;
            if (depth < 0) balanced = false;
        }
    }
    CHECK_EQ(depth, 0);
    CHECK(balanced);
    CHECK(json.find("\"i\": 1") != std::string::npos);
    CHECK(json.find("\"p\": 1") != std::string::npos);
    CHECK(json.find("\"b\": 1") != std::string::npos);
    CHECK(json.find("\"start_nal_offset\": 0") != std::string::npos);
}

void test_text_report_mentions_offsets() {
    const AnalysisResult result = analyze_bytes(generate_reference_sample());
    const std::string text = render_report(result, ReportFormat::Text);
    CHECK(text.find("Image groups:") != std::string::npos);
    CHECK(text.find("group 0 @0") != std::string::npos);
    CHECK(text.find("I=1 P=1 B=1") != std::string::npos);
}

}  // namespace

REGISTER_TEST(test_generated_sample_matches_reference);
REGISTER_TEST(test_interlaced_sample_reports_fields);
REGISTER_TEST(test_json_report_has_required_sections);
REGISTER_TEST(test_text_report_mentions_offsets);
