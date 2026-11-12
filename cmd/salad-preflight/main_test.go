//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrapperHelper(t *testing.T) {
	if os.Getenv("PREFLIGHT_HELPER") != "1" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			os.Args = append([]string{"salad-preflight"}, os.Args[i+1:]...)
			os.Exit(run())
		}
	}
	os.Exit(99)
}

func wrapper(t *testing.T, env []string, args ...string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, append([]string{"-test.run=^TestWrapperHelper$", "--"}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "SALAD_PREFLIGHT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PREFLIGHT_HELPER=1")
	cmd.Env = append(cmd.Env, env...)
	log := &strings.Builder{}
	cmd.Stderr = log
	return cmd, log
}

func TestExecPreservesPIDArgumentsEnvironmentAndExit(t *testing.T) {
	cmd, log := wrapper(t, []string{"PREFLIGHT_APP_TEST=kept"}, "--", "/bin/sh", "-c", `printf '%s\n' "$$" "$1" "$PREFLIGHT_APP_TEST"; exit 17`, "app", "space and $literal")
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 17 {
		t.Fatalf("%v: %s", err, log)
	}
	want := fmt.Sprintf("%d\nspace and $literal\nkept\n", cmd.Process.Pid)
	if string(out) != want {
		t.Fatalf("%q != %q", out, want)
	}
}

func TestConfigurationFailurePreventsLaunch(t *testing.T) {
	cmd, log := wrapper(t, []string{"SALAD_PREFLIGHT_DRY_RUN=invalid-secret"}, "/bin/sh", "-c", "echo must-not-run")
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || len(out) != 0 {
		t.Fatalf("%v %s", err, out)
	}
	if strings.Contains(log.String(), "invalid-secret") {
		t.Fatal("leaked config")
	}
}

func TestDryRunLaunchesAfterMockGPUFailure(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "nvidia-smi")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '1\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd, log := wrapper(t, []string{"SALAD_PREFLIGHT_NVIDIA_SMI=" + fake, "SALAD_PREFLIGHT_GPU_TOTAL_MIN_MIB=100", "SALAD_PREFLIGHT_RETRY_DELAY=0s"}, "/bin/sh", "-c", "echo started")
	out, err := cmd.Output()
	if err != nil || string(out) != "started\n" || !strings.Contains(log.String(), "would_reallocate") {
		t.Fatalf("%v %s %s", err, out, log)
	}
}

func TestUnknownBlockPreventsLaunch(t *testing.T) {
	cmd, _ := wrapper(t, []string{"SALAD_PREFLIGHT_NVIDIA_SMI=/missing", "SALAD_PREFLIGHT_GPU_TOTAL_MIN_MIB=100", "SALAD_PREFLIGHT_UNKNOWN_POLICY=block", "SALAD_PREFLIGHT_RETRY_DELAY=0s"}, "/bin/sh", "-c", "echo must-not-run")
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 || len(out) != 0 {
		t.Fatalf("%v %s", err, out)
	}
}

func TestDryRunReportsAddedChecksWithIdentity(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "nvidia-smi")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '91\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	bench := filepath.Join(dir, "bench")
	if err := os.WriteFile(bench, []byte("#!/bin/sh\necho 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd, log := wrapper(t, []string{
		"SALAD_MACHINE_ID=machine-7",
		"SALAD_PREFLIGHT_NVIDIA_SMI=" + fake, "SALAD_PREFLIGHT_GPU_TEMPERATURE_MAX_CELSIUS=80",
		"SALAD_PREFLIGHT_BENCHMARK_COMMAND=" + bench, "SALAD_PREFLIGHT_BENCHMARK_MIN_SCORE=10",
		"SALAD_PREFLIGHT_RETRY_DELAY=0s",
	}, "/bin/sh", "-c", "echo started")
	out, err := cmd.Output()
	if err != nil || string(out) != "started\n" {
		t.Fatalf("%v %s %s", err, out, log)
	}
	for _, want := range []string{
		`"check":"gpu_temperature","result":"fail","measurement":91,"threshold":80,"units":"celsius","comparison":"at_most"`,
		`"check":"benchmark","result":"fail","measurement":5,"threshold":10,"units":"score"`,
		`"action":"would_reallocate","reason":"salad-preflight/v1 code=multiple_checks_failed checks=gpu_temperature,benchmark: Multiple startup requirements not met"`,
	} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("missing %s in %s", want, log)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		if !strings.Contains(line, `"machine_id":"machine-7"`) {
			t.Fatalf("event without identity: %s", line)
		}
	}
}
