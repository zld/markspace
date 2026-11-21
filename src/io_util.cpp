#include "lut/io_util.hpp"

#include <cerrno>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <unistd.h>

namespace lut {

std::string readBinaryFile(const std::string& path) {
    std::ifstream in(path, std::ios::binary);
    if (!in) {
        throw IoError("cannot open file: " + path);
    }
    std::ostringstream ss;
    ss << in.rdbuf();
    if (in.bad()) {
        throw IoError("failure while reading file: " + path);
    }
    return ss.str();
}

void writeFileAtomic(const std::string& path, const std::string& bytes) {
    const std::filesystem::path target(path);
    std::error_code ec;
    if (target.has_parent_path()) {
        std::filesystem::create_directories(target.parent_path(), ec);
        if (ec) {
            throw IoError("cannot create output directory for " + path + ": " +
                          ec.message());
        }
    }
    static unsigned long counter = 0;
    const std::string tmpPath =
        path + ".tmp." + std::to_string(static_cast<long long>(::getpid())) +
        "." + std::to_string(counter++);

    {
        std::ofstream out(tmpPath, std::ios::binary | std::ios::trunc);
        if (!out) {
            throw IoError("cannot open temporary file: " + tmpPath);
        }
        out.write(bytes.data(), static_cast<std::streamsize>(bytes.size()));
        out.flush();
        if (!out) {
            std::filesystem::remove(tmpPath, ec);
            throw IoError("failure while writing temporary file: " + tmpPath);
        }
    }  // Close before rename so data hits the filesystem.

    std::error_code renameEc;
    std::filesystem::rename(tmpPath, target, renameEc);
    if (renameEc) {
        std::filesystem::remove(tmpPath, ec);
        throw IoError("cannot replace output file " + path + ": " +
                      renameEc.message() + " (" + std::strerror(errno) + ")");
    }
}

}  // namespace lut
