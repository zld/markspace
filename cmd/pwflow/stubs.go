package main

import "context"

func runVerifyCommand(_ context.Context, args []string) error {
	return &cliError{code: exitRejected, err: errNotImplemented("verify")}
}

type notImplemented string

func (n notImplemented) Error() string { return string(n) + " is not implemented yet" }
func errNotImplemented(cmd string) error {
	return notImplemented(cmd)
}
