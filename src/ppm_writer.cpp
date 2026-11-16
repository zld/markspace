#include "ppm_writer.hpp"

#include <cstdio>
#include <fstream>
#include <sys/stat.h>

namespace tl {

namespace {

std::string parent_dir(const std::string& path) {
    const std::size_t slash = path.find_last_of('/');
    if (slash == std::string::npos) return ".";
    if (slash == 0) return "/";
    return path.substr(0, slash);
}

void remove_quiet(const std::string& path) {
    std::remove(path.c_str());
}

}  // namespace

bool validate_output_path(const std::string& path) {
    if (path.empty() || path.size() > kMaxPathLen) return false;
    if (path.find('\0') != std::string::npos) return false;

    std::size_t start = 0;
    while (start < path.size()) {
        const std::size_t slash = path.find('/', start);
        const std::string component =
            path.substr(start, slash == std::string::npos
                                       ? std::string::npos
                                       : slash - start);
        if (component == "..") return false;
        if (slash == std::string::npos) break;
        start = slash + 1;
    }

    struct stat info;
    if (stat(parent_dir(path).c_str(), &info) != 0) return false;
    if ((info.st_mode & S_IFDIR) == 0) return false;
    return true;
}

const char* write_status_message(WriteStatus status) {
    switch (status) {
        case WriteStatus::Ok: return "ok";
        case WriteStatus::InvalidPath: return "invalid output path";
        case WriteStatus::OpenFailed: return "cannot open output file";
        case WriteStatus::WriteFailed: return "write failed (disk full?)";
        case WriteStatus::FlushFailed: return "flush/sync failed";
    }
    return "unknown write error";
}

WriteStatus write_ppm(const std::string& path, const FrameBuffer& frame,
                      std::string* error) {
    auto report = [&](WriteStatus status) {
        if (error) *error = write_status_message(status) + (": " + path);
        return status;
    };
    if (!frame.valid()) return report(WriteStatus::InvalidPath);
    if (!validate_output_path(path))
        return report(WriteStatus::InvalidPath);

    const std::string tmp = path + ".tmp";
    {
        std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
        if (!out.is_open()) {
            remove_quiet(tmp);
            return report(WriteStatus::OpenFailed);
        }
        out << "P6\n" << frame.width() << ' ' << frame.height() << "\n255\n";
        if (!out.good()) {
            out.close();
            remove_quiet(tmp);
            return report(WriteStatus::WriteFailed);
        }
        out.write(reinterpret_cast<const char*>(frame.raw()),
                  static_cast<std::streamsize>(frame.byte_size()));
        if (!out.good()) {
            out.close();
            remove_quiet(tmp);
            return report(WriteStatus::WriteFailed);
        }
        out.flush();
        if (!out.good()) {
            out.close();
            remove_quiet(tmp);
            return report(WriteStatus::FlushFailed);
        }
    }
    if (std::rename(tmp.c_str(), path.c_str()) != 0) {
        remove_quiet(tmp);
        return report(WriteStatus::WriteFailed);
    }
    return WriteStatus::Ok;
}

}  // namespace tl
