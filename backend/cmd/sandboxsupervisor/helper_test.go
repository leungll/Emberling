package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// helperArg makes the test binary act as the supervised command instead of running tests.
// The supervisor gives its command a fixed environment, so the mode is selected by
// argument rather than by an environment variable.
const helperArg = "supervisor-test-helper"

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == helperArg {
		os.Exit(runHelper(os.Args[2], os.Args[3:]))
	}
	os.Exit(m.Run())
}

// helperCommand returns a command line that runs the test binary in helper mode.
func helperCommand(mode string, args ...string) []string {
	return append([]string{os.Args[0], helperArg, mode}, args...)
}

// runHelper implements the helper modes and returns the process exit code.
//
//   - env: prints the sorted environment, one variable per line, and exits 0.
//   - write <hex bytes> <count> <exit code> [<hex suffix>]: writes the bytes count times,
//     then the optional suffix, and exits.
//   - wait-fifo <fifo> <exit code>: reads the FIFO to EOF, prints a line and exits.
//   - group <fifo>: opens the FIFO for writing, starts a child that inherits it, reports
//     "ready" on it and blocks; the FIFO reaches EOF only when both processes are gone.
//   - block: blocks until killed.
func runHelper(mode string, args []string) int {
	switch mode {
	case "env":
		environ := os.Environ()
		slices.Sort(environ)
		for _, entry := range environ {
			_, _ = os.Stdout.WriteString(entry + "\n")
		}
		return 0
	case "write":
		chunk, err := hex.DecodeString(args[0])
		if err != nil {
			return 90
		}
		count, _ := strconv.Atoi(args[1])
		code, _ := strconv.Atoi(args[2])
		var suffix []byte
		if len(args) > 3 {
			if suffix, err = hex.DecodeString(args[3]); err != nil {
				return 90
			}
		}
		_, _ = os.Stdout.Write([]byte(strings.Repeat(string(chunk), count) + string(suffix)))
		return code
	case "wait-fifo":
		content, err := os.ReadFile(args[0])
		if err != nil {
			return 90
		}
		code, _ := strconv.Atoi(args[1])
		_, _ = fmt.Fprintf(os.Stdout, "released after %d bytes\n", len(content))
		return code
	case "group":
		fifo, err := os.OpenFile(args[0], os.O_WRONLY, 0)
		if err != nil {
			return 90
		}
		child := exec.Command(os.Args[0], helperArg, "block")
		child.Stdout = fifo
		if err := child.Start(); err != nil {
			return 91
		}
		if _, err := fifo.WriteString("ready\n"); err != nil {
			return 92
		}
		blockUntilKilled()
		return 0
	case "block":
		blockUntilKilled()
		return 0
	default:
		return 99
	}
}

// blockUntilKilled waits for a signal that is never sent; only SIGKILL ends the process.
// A signal channel keeps the runtime from reporting a deadlock.
func blockUntilKilled() {
	never := make(chan os.Signal, 1)
	signal.Notify(never, syscall.SIGUSR2)
	<-never
}
