#include "demo_factory.hpp"

#include <cstdio>
#include <fstream>

namespace tl {

std::string demo_manifest_text() {
  return
      "# transition_lab demo manifest (generated)\n"
      "canvas 64 48\n"
      "fps 24 1\n"
      "clip gradient 48\n"
      "transition crossfade 24\n"
      "clip checker 48\n"
      "speed 0 12 2/1\n"
      "speed 12 24 1/2\n"
      "transition wipe 24\n"
      "clip shapes 48\n"
      "transition radial 24\n"
      "clip gradient 48\n";
}

bool write_demo(const std::string& path, std::string& err) {
  const std::string tmp = path + ".tmp";
  {
    std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
    if (!out) {
      err = "cannot open " + tmp + " for writing";
      return false;
    }
    out << demo_manifest_text();
    out.flush();
    if (!out) {
      err = "write failed for " + tmp;
      out.close();
      std::remove(tmp.c_str());
      return false;
    }
  }
  if (std::rename(tmp.c_str(), path.c_str()) != 0) {
    err = "rename " + tmp + " -> " + path + " failed";
    std::remove(tmp.c_str());
    return false;
  }
  return true;
}

}  // namespace tl
