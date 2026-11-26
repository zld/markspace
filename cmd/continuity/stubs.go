package main

import "fmt"

func runDemo(_ []string) int {
	fmt.Println("demo command is being assembled")
	return exitGeneric
}

func runPlan(_ []string) int {
	fmt.Println("plan command is being assembled")
	return exitGeneric
}

func runResume(_ []string) int {
	fmt.Println("resume command is being assembled")
	return exitGeneric
}
