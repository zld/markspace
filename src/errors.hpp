#pragma once

#include <stdexcept>
#include <string>
#include <utility>

namespace lutcheck {

// Exit codes are part of the command line contract and are intentionally
// distinct so that build scripts can tell bad arguments from bad data.
enum class ExitCode : int {
  Ok = 0,
  Usage = 2,
  Input = 3,
  Deviation = 4,
  Output = 5,
};

class AppError : public std::runtime_error {
 public:
  AppError(ExitCode code, std::string message)
      : std::runtime_error(std::move(message)), code_(code) {}

  ExitCode code() const noexcept { return code_; }

 private:
  ExitCode code_;
};

[[noreturn]] inline void fail(ExitCode code, const std::string& message) {
  throw AppError(code, message);
}

}  // namespace lutcheck
