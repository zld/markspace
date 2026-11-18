#include "manifest.hpp"

#include <cstdio>
#include <fstream>
#include <sstream>
#include <unistd.h>

#include "errors.hpp"
#include "frame_buffer.hpp"

namespace tr {

namespace {

std::string manifestPath(const std::string& dir) {
  return dir + "/" + kManifestName;
}

void atomicWriteText(const std::string& path, const std::string& body) {
  const std::string tmp = path + ".tmp." + std::to_string(getpid());
  {
    std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
    if (!out) throw IoError("cannot open for write: " + path);
    out << body;
    out.flush();
    if (!out) {
      std::remove(tmp.c_str());
      throw IoError("cannot write manifest: " + path);
    }
  }
  if (std::rename(tmp.c_str(), path.c_str()) != 0) {
    std::remove(tmp.c_str());
    throw IoError("cannot replace manifest atomically: " + path);
  }
}

}  // namespace

void writeManifest(const std::string& dir, const Manifest& m) {
  std::ostringstream os;
  os << "version=1\n"
     << "width=" << m.width << '\n'
     << "height=" << m.height << '\n'
     << "fps=" << m.fpsNum << '/' << m.fpsDen << '\n'
     << "sample=" << m.sample << '\n';
  for (const ManifestEntry& e : m.entries) {
    os << "scene\t" << e.type << '\t' << e.frames << '\t' << e.firstHash
       << '\t' << e.lastHash << '\n';
  }
  atomicWriteText(manifestPath(dir), os.str());
}

Manifest readManifest(const std::string& dir) {
  std::ifstream in(manifestPath(dir));
  if (!in) throw IoError("manifest not found: " + manifestPath(dir));
  Manifest m{};
  std::string line;
  int version = 0;
  while (std::getline(in, line)) {
    if (line.rfind("version=", 0) == 0) version = std::stoi(line.substr(8));
    if (line.rfind("width=", 0) == 0) m.width = std::stoi(line.substr(6));
    if (line.rfind("height=", 0) == 0) m.height = std::stoi(line.substr(7));
    if (line.rfind("fps=", 0) == 0) {
      const std::string v = line.substr(4);
      const auto slash = v.find('/');
      m.fpsNum = std::stoll(v.substr(0, slash));
      m.fpsDen = std::stoll(v.substr(slash + 1));
    }
    if (line.rfind("sample=", 0) == 0) m.sample = line.substr(7);
    if (line.rfind("scene\t", 0) == 0) {
      std::istringstream row(line);
      std::string tag, type, first, last;
      int frames = 0;
      row >> tag >> type >> frames >> first >> last;
      m.entries.push_back(ManifestEntry{type, frames, first, last});
    }
  }
  if (version != 1 || m.width <= 0 || m.height <= 0 || m.fpsDen <= 0 ||
      m.sample.empty() || m.entries.empty()) {
    throw UsageError("manifest is incomplete or unsupported: " +
                     manifestPath(dir));
  }
  return m;
}

}  // namespace tr
