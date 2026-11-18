#include <exception>
#include <iostream>
#include <string>

#include "cli.hpp"
#include "errors.hpp"

int main(int argc, char** argv) {
  if (argc <= 1) {
    std::cerr << lutcheck::usage_text();
    return static_cast<int>(lutcheck::ExitCode::Usage);
  }

  lutcheck::Options options;
  std::string error;
  if (!lutcheck::parse_options(argc, argv, &options, &error)) {
    std::cerr << "lutcheck: " << error << "\n\n";
    std::cerr << lutcheck::usage_text();
    return static_cast<int>(lutcheck::ExitCode::Usage);
  }
  if (options.help) {
    std::cout << lutcheck::usage_text();
    return static_cast<int>(lutcheck::ExitCode::Ok);
  }

  try {
    return lutcheck::run(options);
  } catch (const lutcheck::AppError& failure) {
    std::cerr << "lutcheck: " << failure.what() << '\n';
    return static_cast<int>(failure.code());
  } catch (const std::exception& failure) {
    // Anything not modelled as an AppError is a bug; report it as an output
    // failure rather than pretending it was bad input.
    std::cerr << "lutcheck: internal error: " << failure.what() << '\n';
    return static_cast<int>(lutcheck::ExitCode::Output);
  }
}
