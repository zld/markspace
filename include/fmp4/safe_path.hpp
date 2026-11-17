#pragma once
// Path safety: reject traversal, input/output overlap and symlink escape.
#include <string>

namespace fmp4::path {

// Canonical absolute path of an existing file/dir. Every path component is
// lstat-checked: symlinks inside the tree are rejected (no escape).
std::string canonical_existing(const std::string &p);

// Canonical path for an output target that may not exist yet. Its parent must
// exist and be safe; the basename must not contain '.' or '..' traversal.
std::string canonical_output(const std::string &p);

// Join a manifest-relative path under an already-safe canonical root.
// Rejects absolute paths, '..' traversal, NUL bytes and symlinked targets.
std::string join_under(const std::string &root, const std::string &rel);

// True when child is root itself or located beneath root (lexicographic
// boundary-safe comparison on canonical paths).
bool is_within(const std::string &root_canon,
               const std::string &child_canon);

// Throw Fmp4Error(exit 2 semantics) when the two directory trees overlap.
void ensure_not_overlapping(const std::string &in_canon,
                            const std::string &out_canon);

} // namespace fmp4::path
