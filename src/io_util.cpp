#include "lut/io_util.hpp"

#include <cerrno>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <fcntl.h>
#include <unistd.h>

namespace lut {
namespace {

// fsyncs a directory so the subsequent rename is durable. Best effort: a
// platform that refuses directory fsync still gets the atomic rename.
void syncDirectory(const std::filesystem::path& dir) {
    const int fd = ::open(dir.c_str(), O_RDONLY);
    if (fd < 0) {
        return;
    }
    (void)::fsync(fd);
    ::close(fd);
}

}  // namespace

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

    // Use a raw fd so the data can be fsynced before the rename.
    const int fd = ::open(tmpPath.c_str(), O_WRONLY | O_CREAT | O_TRUNC,
                          0644);
    if (fd < 0) {
        throw IoError("cannot open temporary file: " + tmpPath + " (" +
                      std::strerror(errno) + ")");
    }
    const char* cursor = bytes.data();
    std::size_t remaining = bytes.size();
    bool writeFailed = false;
    while (remaining > 0) {
        const ssize_t written =
            ::write(fd, cursor, static_cast<std::size_t>(remaining));
        if (written < 0) {
            if (errno == EINTR) {
                continue;
            }
            writeFailed = true;
            break;
        }
        cursor += written;
        remaining -= static_cast<std::size_t>(written);
    }
    if (!writeFailed && ::fsync(fd) != 0) {
        writeFailed = true;
    }
    if (::close(fd) != 0 && !writeFailed) {
        writeFailed = true;
    }
    if (writeFailed) {
        std::error_code removeEc;
        std::filesystem::remove(tmpPath, removeEc);
        throw IoError("failure while writing temporary file: " + tmpPath);
    }

    std::error_code renameEc;
    std::filesystem::rename(tmpPath, target, renameEc);
    if (renameEc) {
        std::filesystem::remove(tmpPath, ec);
        throw IoError("cannot replace output file " + path + ": " +
                      renameEc.message() + " (" + std::strerror(errno) + ")");
    }
    if (target.has_parent_path()) {
        syncDirectory(target.parent_path());
    } else {
        syncDirectory(std::filesystem::path("."));
    }
}

}  // namespace lut
