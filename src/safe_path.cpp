#include "fmp4/safe_path.hpp"

#include <filesystem>
#include <system_error>

#include "fmp4/box_reader.hpp"

namespace fs = std::filesystem;

namespace fmp4::path {

static void reject_if_symlink(const fs::path &p) {
  std::error_code ec;
  auto st = fs::symlink_status(p, ec);
  if (ec) throw Fmp4Error("cannot stat path: " + p.string(), p.string(), 0);
  if (fs::is_symlink(st))
    throw Fmp4Error("symlink escape rejected: " + p.string(), p.string(), 0);
}

std::string canonical_existing(const std::string &p) {
  if (p.empty() || p.find('\0') != std::string::npos)
    throw Fmp4Error("invalid path", p, 0);
  // The supplied target itself must not be a symlink; system prefix symlinks
  // (e.g. /var -> /private/var on macOS) are resolved by canonical().
  reject_if_symlink(fs::absolute(p));
  std::error_code ec;
  fs::path canon = fs::canonical(p, ec);
  if (ec || !fs::exists(canon))
    throw Fmp4Error("cannot canonicalize path: " + p, p, 0);
  return canon.string();
}

std::string canonical_output(const std::string &p) {
  if (p.empty() || p.find('\0') != std::string::npos)
    throw Fmp4Error("invalid path", p, 0);
  fs::path norm = fs::absolute(p).lexically_normal();
  if (norm.native().find("..") != std::string::npos)
    throw Fmp4Error("path traversal rejected", p, 0);
  fs::path parent = norm.parent_path();
  if (parent.empty() || !fs::exists(parent))
    throw Fmp4Error("output parent directory does not exist", p, 0);
  // System prefix symlinks (macOS /tmp -> /private/tmp) are resolved by
  // canonical(); tree-internal escape is enforced separately via join_under.
  std::error_code ec;
  fs::path cparent = fs::canonical(parent, ec);
  if (ec) throw Fmp4Error("cannot canonicalize output parent", p, 0);
  // The output target itself, if it already exists, must not be a symlink.
  if (fs::exists(norm)) reject_if_symlink(norm);
  return cparent.string() + "/" + norm.filename().string();
}

std::string join_under(const std::string &root, const std::string &rel) {
  if (rel.empty() || rel.find('\0') != std::string::npos)
    throw Fmp4Error("invalid manifest path", root, 0);
  fs::path rp(rel);
  if (rp.is_absolute())
    throw Fmp4Error("absolute manifest path rejected: " + rel, rel, 0);
  for (const auto &part : rp) {
    if (part == "..")
      throw Fmp4Error("path traversal rejected: " + rel, rel, 0);
  }
  fs::path joined = fs::path(root) / rp;
  fs::path norm = joined.lexically_normal();
  if (!is_within(root, norm.string()))
    throw Fmp4Error("manifest path escapes root: " + rel, rel, 0);
  // Any symlink inside the tree would let a manifest entry escape the root.
  if (fs::exists(norm)) reject_if_symlink(norm);
  return norm.string();
}

bool is_within(const std::string &root_canon,
               const std::string &child_canon) {
  if (child_canon == root_canon) return true;
  std::string prefix = root_canon;
  if (prefix.back() != '/') prefix.push_back('/');
  return child_canon.compare(0, prefix.size(), prefix) == 0;
}

void ensure_not_overlapping(const std::string &in_canon,
                            const std::string &out_canon) {
  if (is_within(in_canon, out_canon) || is_within(out_canon, in_canon))
    throw Fmp4Error("input and output directories must not overlap: " +
                    in_canon + " vs " + out_canon);
}

} // namespace fmp4::path
