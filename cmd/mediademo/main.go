// mediademo is the terminal acceptance entry for the local media kernel.
//
//	mediademo generate --dir D
//	mediademo verify --input F [--trace T] [--range SN/SD:EN/ED]
//	mediademo resume --dir D [--interrupt-after N]
//
// Exit codes: 0 success, 1 argument error, 2 processing fault (corrupt
// input, missing segment, unrepresentable time, blocked dependency).
package main

import (
	"fmt"
	"os"

	"mediakernel/internal/fault"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = cmdGenerate(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "resume":
		err = cmdResume(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		if fe, ok := err.(*fault.Error); ok {
			fmt.Printf("error_code=%s offset=%d message=%q\n", fe.Code, fe.Offset, fe.Msg)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  mediademo generate --dir D
  mediademo verify --input F [--trace T] [--range SN/SD:EN/ED]
  mediademo resume --dir D [--interrupt-after N]`)
}
