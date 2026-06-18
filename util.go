package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PromptResult is returned by a confirm() call.
type PromptResult int

const (
	PromptYes PromptResult = iota
	PromptNo
	PromptBlocked // preview mode: an answer would be needed but none can be given
)

// Confirmer abstracts yes/no prompting so tests (preview mode) don't block.
type Confirmer interface {
	Confirm(question string) PromptResult
}

// TerminalConfirmer reads "y" from stdin (interactive, non-preview).
type TerminalConfirmer struct{}

func (TerminalConfirmer) Confirm(question string) PromptResult {
	fmt.Print(question + " [y/N] ")
	var resp string
	if _, err := fmt.Fscanln(os.Stdin, &resp); err != nil {
		return PromptNo
	}
	resp = strings.TrimSpace(strings.ToLower(resp))
	if resp == "y" || resp == "yes" {
		return PromptYes
	}
	return PromptNo
}

// PreviewConfirmer always blocks (would prompt, but can't answer in preview).
type PreviewConfirmer struct{}

func (PreviewConfirmer) Confirm(question string) PromptResult {
	return PromptBlocked
}

// --- confirm helpers ---

// requireConfirmation asks `question` and returns an error if not confirmed.
func requireConfirmation(cf Confirmer, question string) error {
	switch cf.Confirm(question) {
	case PromptYes:
		return nil
	default: // PromptNo or PromptBlocked
		return errors.New("not confirmed")
	}
}

// --- misc helpers ---

// isBelowOrSamePath reports whether child is the same as, or nested under, parent.
// Both must be absolute.
func isBelowOrSamePath(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	if !strings.HasSuffix(parent, string(os.PathSeparator)) {
		parent += string(os.PathSeparator)
	}
	return strings.HasPrefix(child, parent)
}

// parseSizeGB parses a size like "8G", "8192M", "8GB", "100g" into GiB (integer).
func parseSizeGB(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	last := s[len(s)-1]
	num := s
	switch last {
	case 'g', 'G':
		num = s[:len(s)-1]
	case 'm', 'M':
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, fmt.Errorf("invalid size %q", s)
		}
		return (n + 1023) / 1024, nil // round up to GiB
	default:
		return 0, fmt.Errorf("invalid size suffix in %q (use G or M)", s)
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		// Maybe no suffix but plain number -> assume GiB? Spec uses e.g. 100G; be strict.
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n <= 0 {
		return 0, errors.New("size must be > 0")
	}
	return n, nil
}

// pidIsQemu checks whether a process is a qemu instance (via /proc/<pid>/comm).
func pidIsQemu(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	name := strings.TrimSpace(string(data))
	// comm has no leading "qemu-system-"; match the basename and arch suffix.
	name = strings.TrimSuffix(name, ".exe")
	return name == "qemu-system-x86_64" || name == "qemu-system-aarch64" ||
		strings.HasPrefix(name, "qemu-system-") || name == "qemu-system-x86"
}

// pidAlive reports whether a process with the given pid currently exists.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// process.Signal(0) is the canonical "is it alive" probe.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
