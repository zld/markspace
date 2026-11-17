#include "ppm_writer.hpp"

#include <cstdio>
#include <cstring>
#include <fstream>

namespace tl {

bool path_is_safe(const std::string& p) {
  if (p.empty()) return false;
  size_t i = 0;
  while (i <= p.size()) {
    size_t slash = p.find('/', i);
    std::string comp = p.substr(i, slash == std::string::npos
                                       ? std::string::npos
                                       : slash - i);
    if (comp == "..") return false;
    if (slash == std::string::npos) break;
    i = slash + 1;
  }
  return true;
}

bool write_ppm(const FrameBuffer& fb, const std::string& path,
               std::string& err) {
  const std::string tmp = path + ".tmp";
  {
    std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
    if (!out) {
      err = "cannot open " + tmp + " for writing";
      return false;
    }
    out << "P6\n" << fb.width() << " " << fb.height() << "\n255\n";
    out.write(reinterpret_cast<const char*>(fb.data()),
              static_cast<std::streamsize>(fb.size() * sizeof(RGB)));
    out.flush();
    if (!out) {
      err = "write failed for " + tmp;
      out.close();
      std::remove(tmp.c_str());
      return false;
    }
  }
  if (std::rename(tmp.c_str(), path.c_str()) != 0) {
    err = "rename " + tmp + " -> " + path + " failed: " +
          std::strerror(errno);
    std::remove(tmp.c_str());
    return false;
  }
  return true;
}

namespace {

// Reads one whitespace-separated token, skipping PPM '#' comments.
bool next_token(std::istream& in, std::string& tok) {
  tok.clear();
  char c;
  while (in.get(c)) {
    if (c == '#') {
      std::string dummy;
      std::getline(in, dummy);
      continue;
    }
    if (!std::isspace(static_cast<unsigned char>(c))) {
      tok += c;
      break;
    }
  }
  if (tok.empty()) return false;
  while (in.get(c)) {
    if (std::isspace(static_cast<unsigned char>(c))) break;
    tok += c;
  }
  return true;
}

}  // namespace

bool verify_ppm(const FrameBuffer& fb, const std::string& path,
                std::string& err) {
  std::ifstream in(path, std::ios::binary);
  if (!in) {
    err = "cannot open " + path + " for reading";
    return false;
  }
  std::string magic, sw, sh, smax;
  if (!next_token(in, magic) || magic != "P6") {
    err = path + ": bad magic (expected P6)";
    return false;
  }
  if (!next_token(in, sw) || !next_token(in, sh) || !next_token(in, smax)) {
    err = path + ": truncated header";
    return false;
  }
  long w = std::strtol(sw.c_str(), nullptr, 10);
  long h = std::strtol(sh.c_str(), nullptr, 10);
  long maxval = std::strtol(smax.c_str(), nullptr, 10);
  if (w != static_cast<long>(fb.width()) ||
      h != static_cast<long>(fb.height())) {
    err = path + ": dimension mismatch";
    return false;
  }
  if (maxval != 255) {
    err = path + ": maxval is not 255";
    return false;
  }
  std::vector<RGB> px(fb.size());
  in.read(reinterpret_cast<char*>(px.data()),
          static_cast<std::streamsize>(px.size() * sizeof(RGB)));
  if (!in) {
    err = path + ": pixel data truncated";
    return false;
  }
  if (std::memcmp(px.data(), fb.data(), px.size() * sizeof(RGB)) != 0) {
    err = path + ": pixel data mismatch";
    return false;
  }
  return true;
}

}  // namespace tl
