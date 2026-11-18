// Tests for the bytes module.
#include <cstdio>
#include <string>
#include <vector>

#include "byte_source.hpp"
#include "test_util.hpp"
#include "registry.hpp"

using namespace h264inspect;

namespace {

void test_reads_bytes_and_offsets() {
    const std::string path = selftest::write_temp("h264inspect_bytes.bin", std::string("\x00\x01\x02\x03\xff", 5));
    ByteSource source;
    Diagnostic err;
    CHECK(source.open(path, ByteSource::kDefaultMaxSize, &err));
    CHECK_EQ(source.size(), std::uint64_t{5});
    CHECK_EQ(int(source.data()[0]), 0);
    CHECK_EQ(int(source.data()[4]), 0xff);

    std::vector<std::uint8_t> out;
    CHECK(source.read(1, 3, out));
    CHECK_EQ(out.size(), std::size_t{3});
    CHECK_EQ(int(out[0]), 1);
    CHECK_EQ(int(out[2]), 3);
    CHECK(!source.read(3, 3, out));
    CHECK(!source.read(6, 0, out));
    CHECK(source.read(5, 0, out));
    std::remove(path.c_str());
}

void test_missing_file_reports_io() {
    ByteSource source;
    Diagnostic err;
    CHECK(!source.open("/tmp/h264inspect_definitely_missing.bin",
                       ByteSource::kDefaultMaxSize, &err));
    CHECK(err.kind == ErrorKind::Io);
    CHECK(!err.message.empty());
}

void test_size_limit() {
    const std::string path = selftest::write_temp("h264inspect_limit.bin", std::string(16, 'x'));
    ByteSource source;
    Diagnostic err;
    CHECK(!source.open(path, 15, &err));
    CHECK(err.kind == ErrorKind::LimitExceeded);
    std::remove(path.c_str());
}

void test_empty_file() {
    const std::string path = selftest::write_temp("h264inspect_empty.bin", std::string());
    ByteSource source;
    Diagnostic err;
    CHECK(source.open(path, ByteSource::kDefaultMaxSize, &err));
    CHECK(source.empty());
    std::remove(path.c_str());
}

}  // namespace

REGISTER_TEST(test_reads_bytes_and_offsets);
REGISTER_TEST(test_missing_file_reports_io);
REGISTER_TEST(test_size_limit);
REGISTER_TEST(test_empty_file);
