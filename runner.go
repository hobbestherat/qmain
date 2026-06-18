package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// ExecResult holds the output of a command run via the host shell.
type ExecResult struct {
	Cmd    string
	Stdout string
	Stderr string
	Err    error
}

// Runner abstracts command execution so tests can intercept and assert calls.
type Runner interface {
	Run(name string, args ...string) ExecResult
}

// ShellRunner executes real commands on the host. Commands are never echoed
// here; callers print what they ran via preview().
type ShellRunner struct{}

// Run runs a command and captures stdout/stderr separately.
func (ShellRunner) Run(name string, args ...string) ExecResult {
	out, err := exec.Command(name, args...).Output()
	res := ExecResult{Cmd: name + " " + strings.Join(args, " ")}
	if len(out) > 0 {
		res.Stdout = strings.TrimRight(string(out), "\n")
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.Stderr = strings.TrimRight(string(ee.Stderr), "\n")
			res.Err = fmt.Errorf("%s", res.Stderr)
		} else {
			res.Err = err
		}
	}
	return res
}
