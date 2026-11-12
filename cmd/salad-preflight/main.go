//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/SaladTechnologies/salad-cloud-preflight/internal/preflight"
)

func main() { os.Exit(run()) }

func run() int {
	encoder := json.NewEncoder(os.Stderr)
	emit := preflight.Identify(os.Environ()).Attach(func(e preflight.Event) { _ = encoder.Encode(e) })
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		emit(preflight.Event{Event: "configuration_error", Action: "block", Detail: "usage: salad-preflight -- command [args...]"})
		return 2
	}
	c, err := preflight.Parse(os.Environ())
	if err != nil {
		emit(preflight.Event{Event: "configuration_error", Action: "block", Detail: err.Error()})
		return 2
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		emit(preflight.Event{Event: "launch_error", Action: "block", Detail: "application_not_found"})
		return 127
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	decision := preflight.Run(ctx, c, preflight.NewProbe(c), preflight.RequestReallocation, emit)
	if !decision.Launch {
		return decision.ExitCode
	}
	if ctx.Err() != nil {
		return 24
	}
	// Replace the wrapper, preserving PID, arguments, environment, working
	// directory, stdio, application exit code and ordinary signal handling.
	stop()
	if syscall.Exec(path, args, os.Environ()) != nil {
		emit(preflight.Event{Event: "launch_error", Action: "block", Detail: "application_exec_failed"})
		return 126
	}
	return 0
}
