package main

// Subcommand stubs; replaced as each module lands.

func cmdFixture(args []string) int { return notImplemented("fixture") }
func cmdInspect(args []string) int { return notImplemented("inspect") }
func cmdLocate(args []string) int  { return notImplemented("locate") }
func cmdExport(args []string) int  { return notImplemented("export") }

func notImplemented(name string) int {
	println("mp4seek: subcommand not yet wired:", name)
	return exitArgs
}
