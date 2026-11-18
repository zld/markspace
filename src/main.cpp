#include <iostream>

#include "errors.hpp"

int main(int argc, char** argv) {
  try {
    (void)argc;
    (void)argv;
    std::cout << "lutcheck: scaffolding\n";
    return static_cast<int>(lutcheck::ExitCode::Ok);
  } catch (const lutcheck::AppError& error) {
    std::cerr << "lutcheck: " << error.what() << '\n';
    return static_cast<int>(error.code());
  } catch (const std::exception& error) {
    std::cerr << "lutcheck: internal error: " << error.what() << '\n';
    return static_cast<int>(lutcheck::ExitCode::Output);
  }
}
