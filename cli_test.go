package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary builds the qmain binary into a temp dir and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "qmain")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// runCLI runs the qmain binary with the given args in workdir, returning
// stdout, stderr and the exit code.
func runCLI(t *testing.T, bin, cfg, workdir string, args ...string) (string, string, int) {
	t.Helper()
	full := append([]string{"--alt-config", cfg}, args...)
	cmd := exec.Command(bin, full...)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin, err := cmd.StdinPipe(); err == nil {
		// Provide empty stdin so interactive prompts default to "No".
		go func() {
			// write a newline and close
			_, _ = stdin.Write([]byte("\n"))
			_ = stdin.Close()
		}()
	}
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run %v: %v", full, err)
		}
	}
	return stdout.String(), stderr.String(), code
}

// TestCLIFullCycle walks through a representative lifecycle of disks and VMs
// using real process invocations against a temp config file, exercising the
// --preview mode as well.
func TestCLIFullCycle(t *testing.T) {
	bin := buildBinary(t)
	workdir := t.TempDir()
	cfg := filepath.Join(workdir, "config.json")
	var out, stderr string
	var code int

	// create-disk disk1 100G
	out, _, code = runCLI(t, bin, cfg, workdir, "create-disk", "disk1", "100G")
	if code != 0 {
		t.Fatalf("create-disk: code=%d out=%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(workdir, "disk1.qcow2")); err != nil {
		t.Fatalf("disk image not created: %v", err)
	}

	// Preview create-vm (should not modify config)
	out, _, code = runCLI(t, bin, cfg, workdir, "--preview", "create-vm", "vm1")
	if code != 0 {
		t.Fatalf("preview create-vm: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "[preview]") || !strings.Contains(out, "vm1") {
		t.Fatalf("preview output unexpected: %s", out)
	}
	// config should be unchanged (no vms)
	c := load(t, cfg)
	if len(c.VMs) != 0 {
		t.Fatalf("preview should not modify config, got %d vms", len(c.VMs))
	}

	// create-vm vm1 (defaults: disk=disk1 last, ram=8G, arch=x86_64)
	out, _, code = runCLI(t, bin, cfg, workdir, "create-vm", "vm1")
	if code != 0 {
		t.Fatalf("create-vm: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "ssh-port=2222") {
		t.Fatalf("expected ssh-port=2222 in output: %s", out)
	}

	// create-vm vm2 should get a different port
	out, _, code = runCLI(t, bin, cfg, workdir, "create-vm", "vm2")
	if code != 0 {
		t.Fatalf("create-vm2: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "ssh-port=2223") {
		t.Fatalf("expected ssh-port=2223: %s", out)
	}

	// get-ssh vm1
	out, _, code = runCLI(t, bin, cfg, workdir, "get-ssh", "vm1")
	if code != 0 {
		t.Fatalf("get-ssh: code=%d", code)
	}
	if strings.TrimSpace(out) != "ssh -p 2222 root@127.0.0.1" {
		t.Fatalf("get-ssh output: %q", out)
	}

	// list
	out, _, code = runCLI(t, bin, cfg, workdir, "list")
	if code != 0 {
		t.Fatalf("list: code=%d", code)
	}
	if !strings.Contains(out, "vm1") || !strings.Contains(out, "vm2") {
		t.Fatalf("list output: %s", out)
	}

	// list-disks
	out, _, code = runCLI(t, bin, cfg, workdir, "list-disks")
	if code != 0 {
		t.Fatalf("list-disks: code=%d", code)
	}
	if !strings.Contains(out, "disk1") {
		t.Fatalf("list-disks output: %s", out)
	}

	// delete-disk while VM depends on it -> error
	out, stderr, code = runCLI(t, bin, cfg, workdir, "delete-disk", "disk1")
	if code == 0 {
		t.Fatalf("expected error deleting disk with dependent VM, got code 0")
	}
	if !strings.Contains(out+stderr, "depend") {
		t.Fatalf("expected 'depend' in error, got: %s / %s", out, stderr)
	}

	// freeze disk1
	out, _, code = runCLI(t, bin, cfg, workdir, "freeze-disk", "disk1")
	if code != 0 {
		t.Fatalf("freeze-disk: code=%d out=%s", code, out)
	}

	// run vm1 -> disk is frozen -> error
	out, _, code = runCLI(t, bin, cfg, workdir, "run", "vm1")
	if code == 0 {
		t.Fatalf("expected error running VM with frozen disk")
	}

	// clone-disk from non-frozen base -> create a second disk first.
	runCLI(t, bin, cfg, workdir, "create-disk", "disk2", "20G")
	out, stderr, code = runCLI(t, bin, cfg, workdir, "clone-disk", "clone1", "--base", "disk2")
	if code == 0 {
		t.Fatalf("expected error cloning non-frozen disk, out=%s", out)
	}
	if !strings.Contains(out+stderr, "not frozen") {
		t.Fatalf("expected 'not frozen' error, got: %s / %s", out, stderr)
	}

	// freeze disk2 then clone
	runCLI(t, bin, cfg, workdir, "freeze-disk", "disk2")
	out, _, code = runCLI(t, bin, cfg, workdir, "clone-disk", "clone1", "--base", "disk2")
	if code != 0 {
		t.Fatalf("clone-disk: code=%d out=%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(workdir, "clone1.qcow2")); err != nil {
		t.Fatalf("clone image not created: %v", err)
	}

	// unfreeze disk2 with dependent clone -> error listing dependents
	out, stderr, code = runCLI(t, bin, cfg, workdir, "unfreeze-disk", "disk2")
	if code == 0 {
		t.Fatalf("expected error unfreezing disk with clone")
	}
	if !strings.Contains(out+stderr, "clone1") {
		t.Fatalf("expected dependent clone 'clone1' in error: %s / %s", out, stderr)
	}

	// preview delete-disk clone1
	out, _, code = runCLI(t, bin, cfg, workdir, "--preview", "delete-disk", "clone1")
	if code != 0 {
		t.Fatalf("preview delete-disk: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "[preview]") {
		t.Fatalf("preview output unexpected: %s", out)
	}

	// delete vm1 (auto-confirm "No" via empty stdin -> declined -> error)
	out, _, code = runCLI(t, bin, cfg, workdir, "delete", "vm1")
	if code == 0 {
		t.Fatalf("expected delete to be declined (No), got code 0")
	}

	// delete vm1 with "y" on stdin -> succeeds
	full := []string{"--alt-config", cfg, "delete", "vm1"}
	cmd := exec.Command(bin, full...)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader("y\n")
	var o2, e2 bytes.Buffer
	cmd.Stdout = &o2
	cmd.Stderr = &e2
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 0 {
			t.Fatalf("delete vm1 (yes): %v stderr=%s", err, e2.String())
		}
	}
	c = load(t, cfg)
	if _, ok := c.VMs["vm1"]; ok {
		t.Fatalf("vm1 should be deleted")
	}
}

// TestCLIPreviewChain checks preview mode across many commands without side effects.
func TestCLIPreviewChain(t *testing.T) {
	bin := buildBinary(t)
	workdir := t.TempDir()
	cfg := filepath.Join(workdir, "config.json")

	steps := [][]string{
		{"--preview", "create-disk", "d1", "10G"},
		{"--preview", "create-vm", "v1"},
		{"--preview", "run", "v1"},
		{"--preview", "get-ssh", "v1"},
		{"--preview", "freeze-disk", "d1"},
		{"--preview", "delete-disk", "d1"},
		{"--preview", "kill", "v1"},
		{"--preview", "clear", "v1"},
		{"--preview", "list"},
		{"--preview", "list-disks"},
	}
	for _, args := range steps {
		out, _, code := runCLI(t, bin, cfg, workdir, args...)
		// Many of these preview commands operate on empty config and should
		// still produce output (preview or a computed error), not crash.
		_ = out
		_ = code
	}
	// Config should be empty (all previews).
	c := load(t, cfg)
	if len(c.Disks) != 0 || len(c.VMs) != 0 {
		t.Fatalf("preview chain modified config: disks=%d vms=%d", len(c.Disks), len(c.VMs))
	}
}

func TestCLIUsage(t *testing.T) {
	bin := buildBinary(t)
	_, _, code := runCLI(t, bin, "", t.TempDir())
	if code == 0 {
		t.Fatalf("expected non-zero exit for no args")
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	bin := buildBinary(t)
	_, stderr, code := runCLI(t, bin, filepath.Join(t.TempDir(), "c.json"), t.TempDir(), "bogus")
	if code == 0 {
		t.Fatalf("expected non-zero for unknown command")
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Fatalf("expected 'unknown command' error, got: %s", stderr)
	}
}

func TestCLICreateVMExplicitDisk(t *testing.T) {
	bin := buildBinary(t)
	workdir := t.TempDir()
	cfg := filepath.Join(workdir, "config.json")
	runCLI(t, bin, cfg, workdir, "create-disk", "first", "5G")
	runCLI(t, bin, cfg, workdir, "create-disk", "second", "5G")
	out, _, code := runCLI(t, bin, cfg, workdir, "create-vm", "v1", "--disk", "first")
	if code != 0 {
		t.Fatalf("create-vm --disk: code=%d out=%s", code, out)
	}
	c := load(t, cfg)
	if c.VMs["v1"].Disk != "first" {
		t.Fatalf("expected disk=first, got %q", c.VMs["v1"].Disk)
	}
}

// TestCLIDeleteBaseWithClone verifies deleting a frozen base that has clones is refused.
func TestCLIDeleteBaseWithClone(t *testing.T) {
	bin := buildBinary(t)
	workdir := t.TempDir()
	cfg := filepath.Join(workdir, "config.json")
	runCLI(t, bin, cfg, workdir, "create-disk", "base", "10G")
	runCLI(t, bin, cfg, workdir, "freeze-disk", "base")
	runCLI(t, bin, cfg, workdir, "clone-disk", "child", "--base", "base")
	out, stderr, code := runCLI(t, bin, cfg, workdir, "delete-disk", "base")
	if code == 0 {
		t.Fatalf("expected error deleting base disk with clone, out=%s", out)
	}
	if !strings.Contains(out+stderr, "depend") {
		t.Fatalf("expected 'depend' in error, got: %s / %s", out, stderr)
	}
	// base disk file should still exist.
	if _, err := os.Stat(filepath.Join(workdir, "base.qcow2")); err != nil {
		t.Fatalf("base disk file should remain: %v", err)
	}
}

// Smoke test for parseSizeGB edge cases at the CLI boundary.
func TestCLIBadSize(t *testing.T) {
	bin := buildBinary(t)
	workdir := t.TempDir()
	cfg := filepath.Join(workdir, "config.json")
	_, stderr, code := runCLI(t, bin, cfg, workdir, "create-disk", "d", "badsize")
	if code == 0 {
		t.Fatalf("expected error for bad size")
	}
	if !strings.Contains(stderr, "size") {
		t.Fatalf("expected size error, got: %s", stderr)
	}
}

// TestCLISetVM exercises changing stored VM parameters and that a bare `run`
// then reflects them (via --preview), plus that the global flag parser does not
// consume args after "--".
func TestCLISetVM(t *testing.T) {
	bin := buildBinary(t)
	workdir := t.TempDir()
	cfg := filepath.Join(workdir, "config.json")

	runCLI(t, bin, cfg, workdir, "create-disk", "d", "10G")
	runCLI(t, bin, cfg, workdir, "create-vm", "vm1")

	// Change cpus + display and store extra args after "--".
	out, stderr, code := runCLI(t, bin, cfg, workdir, "set-vm", "vm1",
		"--cpus", "4", "--display", "none", "--", "-cpu", "host")
	if code != 0 {
		t.Fatalf("set-vm: code=%d out=%s err=%s", code, out, stderr)
	}
	if !strings.Contains(out, "cpus=4") || !strings.Contains(out, "display=none") {
		t.Fatalf("set-vm output unexpected: %s", out)
	}

	// A bare run must now reflect the stored params (and the "-- -cpu host"
	// passthrough must survive global flag parsing).
	out, _, code = runCLI(t, bin, cfg, workdir, "--preview", "run", "vm1")
	if code != 0 {
		t.Fatalf("preview run: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "-smp 4") || !strings.Contains(out, "-display none") ||
		!strings.HasSuffix(strings.TrimSpace(out), "-cpu host") {
		t.Fatalf("stored params not applied on bare run: %s", out)
	}

	// set-vm with no flags is an error (nothing to change).
	_, _, code = runCLI(t, bin, cfg, workdir, "set-vm", "vm1")
	if code == 0 {
		t.Fatalf("expected error for set-vm with no params")
	}
}

var _ = fmt.Sprintf // keep fmt import referenced
