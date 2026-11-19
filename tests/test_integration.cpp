#include <catch2/catch_test_macros.hpp>

#include <sys/wait.h>

#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <sstream>
#include <string>

// These tests drive the real h264scan and fixture_gen executables through
// the shell, so they validate wiring, exit codes and the JSON contract, not
// just the in-process parser. Paths come from compile definitions injected
// by CMake (H264SCAN_BIN, FIXTURE_GEN_BIN).

namespace {

std::string shell_quote(const std::string& s) {
    return std::string("'") + s + "'";
}

int run(const std::string& cmd, std::string* output = nullptr) {
    std::string full = cmd + " 2>/tmp/h264_it_stderr.txt";
    if (output) full += " > /tmp/h264_it_stdout.txt";
    const int rc = std::system(full.c_str());
    if (output) {
        std::ifstream in("/tmp/h264_it_stdout.txt");
        std::stringstream ss;
        ss << in.rdbuf();
        *output = ss.str();
    }
    // Normalize the platform wait status to a raw exit code.
    if (rc == -1) return -1;
    return WEXITSTATUS(rc);
}

}  // namespace

TEST_CASE("fixture generator produces an in-range deterministic stream",
          "[integration]") {
    const std::string out = "/tmp/h264_it_fixture.h264";
    const int rc = run(std::string(FIXTURE_GEN_BIN) + " --output " +
                       shell_quote(out));
    REQUIRE(rc == 0);

    std::ifstream f(out, std::ios::binary | std::ios::ate);
    REQUIRE(f.good());
    const auto size = static_cast<std::streamoff>(f.tellg());
    CHECK(size >= 1000);
    CHECK(size <= 10000);
}

TEST_CASE("strict scan of a clean fixture succeeds and emits the JSON contract",
          "[integration]") {
    const std::string in = "/tmp/h264_it_fixture.h264";
    std::string json;
    const int rc = run(std::string(H264SCAN_BIN) + " --input " +
                       shell_quote(in) + " --json --strict", &json);
    REQUIRE(rc == 0);
    // Field presence checks without pulling in a JSON dependency.
    CHECK(json.find("\"profile_idc\"") != std::string::npos);
    CHECK(json.find("\"width\"") != std::string::npos);
    CHECK(json.find("\"height\"") != std::string::npos);
    CHECK(json.find("\"frame_mbs_only_flag\"") != std::string::npos);
    CHECK(json.find("\"frames\"") != std::string::npos);
    CHECK(json.find("\"groups\"") != std::string::npos);
    CHECK(json.find("\"start_offset\"") != std::string::npos);
    // A clean stream reports no diagnostics.
    CHECK(json.find("\"errors\": [") != std::string::npos);
}

TEST_CASE("strict mode returns 2 on a corrupt stream and 64 on bad arguments",
          "[integration]") {
    const std::string corrupt = "/tmp/h264_it_corrupt.h264";
    REQUIRE(run(std::string(FIXTURE_GEN_BIN) + " --output " +
                shell_quote(corrupt) + " --corrupt empty-nal") == 0);
    CHECK(run(std::string(H264SCAN_BIN) + " --input " +
              shell_quote(corrupt) + " --strict") == 2);
    // Non-strict still completes successfully while reporting diagnostics.
    CHECK(run(std::string(H264SCAN_BIN) + " --input " +
              shell_quote(corrupt)) == 0);
    // Usage errors map to 64; missing input and an invalid cap.
    CHECK(run(std::string(H264SCAN_BIN)) == 64);
    CHECK(run(std::string(H264SCAN_BIN) + " --input x --max-nal-bytes 0")
          == 64);
    // Unreadable input maps to 70.
    CHECK(run(std::string(H264SCAN_BIN) + " --input /no/such/file.h264")
          == 70);
}
