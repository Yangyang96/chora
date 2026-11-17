package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/Yangyang96/chora/internal/guardian"
)

func main() {
	if runtime.GOOS != "linux" || os.Getpid() != 1 || os.Geteuid() != 0 || len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "guardian requires dedicated Linux PID1 and fixed policy")
		os.Exit(78)
	}
	a, e1 := time.Parse(time.RFC3339Nano, os.Args[1])
	d, e2 := time.Parse(time.RFC3339Nano, os.Args[2])
	if e1 != nil || e2 != nil {
		os.Exit(78)
	}
	if guardian.Run(context.Background(), guardian.Policy{AuthorizedAt: a, Deadline: d, Lease: 2 * time.Second}, guardian.BootClock, os.Stdin, os.Stdout) != nil {
		os.Exit(78)
	}
}
