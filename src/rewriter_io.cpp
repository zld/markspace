#include "rewriter.hpp"

#include <filesystem>
#include <fstream>

namespace mp4 {

namespace fs = std::filesystem;

namespace {

constexpr const char* TEMP_PREFIX = ".mp4repack.tmp.";
constexpr const char* BACKUP_PREFIX = ".mp4repack.bak.";

void write_bytes(const fs::path& path, const std::vector<uint8_t>& bytes) {
    std::ofstream f(path, std::ios::binary | std::ios::trunc);
    if (!f) throw RewriteError("cannot create temp file: " + path.string());
    if (!bytes.empty() &&
        !f.write(reinterpret_cast<const char*>(bytes.data()),
                 static_cast<std::streamsize>(bytes.size())))
        throw RewriteError("write failed: " + path.string());
    f.flush();
    if (!f) throw RewriteError("flush failed: " + path.string());
}

std::vector<uint8_t> reread(const fs::path& path, const Limits& lim) {
    return read_file(path.string(), {lim.max_file_size});
}

void clean_prefixed(const fs::path& dir, const char* prefix,
                    std::vector<std::string>& removed) {
    std::error_code ec;
    for (const auto& e : fs::directory_iterator(dir, ec)) {
        if (ec) break;
        if (e.is_regular_file() &&
            e.path().filename().string().rfind(prefix, 0) == 0) {
            fs::remove(e.path(), ec);
            if (!ec) removed.push_back(e.path().filename().string());
        }
    }
}

}  // namespace

WriteOutcome write_package_atomic(const RewriteResult& result,
                                  const std::string& output_dir,
                                  const Limits& lim) {
    fs::path dir(output_dir);
    std::error_code ec;
    fs::create_directories(dir, ec);
    if (ec) throw RewriteError("cannot create output directory " +
                               output_dir + ": " + ec.message());

    WriteOutcome outcome;
    clean_prefixed(dir, TEMP_PREFIX, outcome.removed_stale_temps);
    clean_prefixed(dir, BACKUP_PREFIX, outcome.removed_stale_temps);

    struct Stage {
        std::string name;
        uint32_t sequence;
        fs::path temp;
        const std::vector<uint8_t>* bytes;
        bool existed;
        fs::path backup;
    };
    std::vector<Stage> stages;
    stages.reserve(1 + result.fragments.size());

    auto stage = [&](const std::string& name, uint32_t sequence,
                     const std::vector<uint8_t>& bytes) {
        Stage s;
        s.name = name;
        s.sequence = sequence;
        s.bytes = &bytes;
        s.temp = dir / (std::string(TEMP_PREFIX) + name);
        s.existed = fs::exists(dir / name);
        stages.push_back(std::move(s));
    };
    stage(result.init_segment.name, 0, result.init_segment.bytes);
    for (const OutputFile& f : result.fragments)
        stage(f.name, f.sequence, f.bytes);

    // Phase 1: write and verify every temp file. Nothing existing is touched.
    for (Stage& s : stages) {
        if (s.bytes->size() > lim.max_file_size)
            throw RewriteError("output exceeds 2 GiB limit: " + s.name);
        write_bytes(s.temp, *s.bytes);
        bool verified = false;
        try {
            std::vector<uint8_t> back = reread(s.temp, lim);
            verified = (back == *s.bytes) && verify_output_bytes(back, lim);
        } catch (const std::exception&) {
            verified = false;
        }
        if (!verified) {
            std::error_code rm;
            fs::remove(s.temp, rm);
            throw RewriteError("pre-write verification failed for " + s.name +
                               "; original target preserved");
        }
    }

    // Phase 2: swap targets one at a time, backing up the original first so a
    // failure mid-way can be rolled back.
    for (Stage& s : stages) {
        fs::path target = dir / s.name;
        if (s.existed) {
            s.backup = dir / (std::string(BACKUP_PREFIX) + s.name);
            fs::rename(target, s.backup, ec);
            if (ec) {
                throw RewriteError("could not back up existing " + s.name +
                                   ": " + ec.message());
            }
        }
        fs::rename(s.temp, target, ec);
        if (ec) {
            if (s.existed) {
                std::error_code restore;
                fs::rename(s.backup, target, restore);
            }
            throw RewriteError("atomic replace failed for " + s.name + ": " +
                               ec.message());
        }
        if (s.existed) {
            std::error_code rm;
            fs::remove(s.backup, rm);
        }
        outcome.files.push_back(
            {s.name, s.bytes->size(), s.sequence, s.existed});
    }
    return outcome;
}

}  // namespace mp4
