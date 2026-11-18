// Shared helpers for writing inputs to temporary files.
#pragma once

#include <cstdio>
#include <string>

#include "test_harness.hpp"

namespace selftest {

inline std::string write_temp(const std::string& name, const std::string& bytes) {
    const std::string path = std::string("/tmp/") + name;
    std::FILE* f = std::fopen(path.c_str(), "wb");
    CHECK(f != nullptr);
    if (f == nullptr) return path;
    if (!bytes.empty()) {
        CHECK_EQ(std::fwrite(bytes.data(), 1, bytes.size(), f), bytes.size());
    }
    std::fclose(f);
    return path;
}

}  // namespace selftest
