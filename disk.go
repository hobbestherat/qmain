package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// diskPath returns the absolute path to a disk's .qcow2 file, stored in the
// current working directory (where qmain was invoked).
func diskPath(name string) string {
	abs, err := filepath.Abs(name + ".qcow2")
	if err != nil {
		return name + ".qcow2"
	}
	return abs
}

// getDisk returns the disk named `name` or an error if it doesn't exist.
func getDisk(c *Config, name string) (*Disk, error) {
	d, ok := c.Disks[name]
	if !ok {
		return nil, fmt.Errorf("disk %q does not exist", name)
	}
	return d, nil
}

// --- create-disk ---

// CreateDisk creates a fresh qcow2 image and registers it. size is e.g. "100G".
func CreateDisk(r Runner, c *Config, name, size string) (*Disk, error) {
	if name == "" {
		return nil, errors.New("disk name is required")
	}
	if _, ok := c.Disks[name]; ok {
		return nil, fmt.Errorf("disk %q already exists", name)
	}
	if err := validateSize(size); err != nil {
		return nil, err
	}
	path := diskPath(name)
	res := r.Run("qemu-img", "create", "-f", "qcow2", path, size)
	if res.Err != nil {
		return nil, fmt.Errorf("qemu-img create failed: %v", res.Err)
	}
	d := &Disk{
		Name:    name,
		Path:    path,
		Size:    size,
		Created: nowNanos(),
	}
	c.Disks[name] = d
	return d, nil
}

// --- clone-disk ---

// CloneDisk creates a new qcow2 with a backing file (base must be frozen).
func CloneDisk(r Runner, c *Config, name, baseName string) (*Disk, error) {
	if name == "" {
		return nil, errors.New("disk name is required")
	}
	if _, ok := c.Disks[name]; ok {
		return nil, fmt.Errorf("disk %q already exists", name)
	}
	base, err := getDisk(c, baseName)
	if err != nil {
		return nil, err
	}
	if !base.Frozen {
		return nil, fmt.Errorf("base disk %q is not frozen; freeze it first", baseName)
	}

	baseDir := filepath.Dir(base.Path)
	newPath := diskPath(name)
	newDir := filepath.Dir(newPath)

	// Path safety: warn+confirm if the new disk is not at or below the base disk's dir.
	if !isBelowOrSamePath(baseDir, newPath) {
		// Complain: new disk should be in the same path or a folder downward.
		return nil, fmt.Errorf("clone target %q is not in or below the base disk directory %q",
			newDir, baseDir)
	}

	res := r.Run("qemu-img", "create", "-f", "qcow2",
		"-b", base.Path, "-F", "qcow2", newPath)
	if res.Err != nil {
		return nil, fmt.Errorf("qemu-img create (clone) failed: %v", res.Err)
	}
	d := &Disk{
		Name:    name,
		Path:    newPath,
		Base:    baseName,
		Created: nowNanos(),
	}
	c.Disks[name] = d
	return d, nil
}

// --- freeze / unfreeze ---

// FreezeDisk marks a disk read-only (chmod) and frozen in config.
func FreezeDisk(r Runner, c *Config, name string) (*Disk, error) {
	d, err := getDisk(c, name)
	if err != nil {
		return nil, err
	}
	if d.Frozen {
		return d, fmt.Errorf("disk %q is already frozen", name)
	}
	if err := os.Chmod(d.Path, 0o444); err != nil && !os.IsNotExist(err) {
		return d, fmt.Errorf("chmod failed: %v", err)
	}
	d.Frozen = true
	return d, nil
}

// UnfreezeDisk makes a disk writable again unless clones depend on it.
func UnfreezeDisk(r Runner, c *Config, name string) (*Disk, error) {
	d, err := getDisk(c, name)
	if err != nil {
		return nil, err
	}
	if !d.Frozen {
		return d, fmt.Errorf("disk %q is not frozen", name)
	}
	if deps := c.disksWithBase(name); len(deps) > 0 {
		names := make([]string, len(deps))
		for i, dep := range deps {
			names[i] = dep.Name
		}
		return d, fmt.Errorf("cannot unfreeze %q: cloned disks depend on it: %s",
			name, joinNames(names))
	}
	if err := os.Chmod(d.Path, 0o644); err != nil && !os.IsNotExist(err) {
		return d, fmt.Errorf("chmod failed: %v", err)
	}
	d.Frozen = false
	return d, nil
}

// --- delete-disk ---

// DeleteDisk removes a disk's config entry, with VM-dependency and confirmation
// checks. It asks twice: first to confirm removing the disk from qmain, then
// whether the underlying image file should also be deleted from disk (answering
// no keeps the file and only detaches it). The returned bool reports whether the
// image file was deleted.
func DeleteDisk(r Runner, c *Config, cf Confirmer, name string) (*Disk, bool, error) {
	d, err := getDisk(c, name)
	if err != nil {
		return nil, false, err
	}
	if vms := c.vmsUsingDisk(name); len(vms) > 0 {
		names := make([]string, len(vms))
		for i, v := range vms {
			names[i] = v.Name
		}
		return d, false, fmt.Errorf("cannot delete %q: VMs depend on it: %s",
			name, joinNames(names))
	}
	// A base disk with cloned children would break those clones; refuse.
	if clones := c.disksWithBase(name); len(clones) > 0 {
		names := make([]string, len(clones))
		for i, cl := range clones {
			names[i] = cl.Name
		}
		return d, false, fmt.Errorf("cannot delete %q: cloned disks depend on it: %s",
			name, joinNames(names))
	}
	if err := requireConfirmation(cf, fmt.Sprintf("Remove disk %q from qmain?", name)); err != nil {
		return d, false, err
	}
	// Second question: also delete the image file, or just detach it from qmain?
	deleteFile := cf.Confirm(fmt.Sprintf(
		"Also delete the image file %s? (No keeps the file and only detaches it)", d.Path)) == PromptYes
	if deleteFile {
		// If frozen, make it writable again before removing the file.
		if d.Frozen {
			if err := os.Chmod(d.Path, 0o644); err != nil && !os.IsNotExist(err) {
				return d, false, fmt.Errorf("chmod failed: %v", err)
			}
			d.Frozen = false
		}
		if err := os.Remove(d.Path); err != nil && !os.IsNotExist(err) {
			return d, false, fmt.Errorf("remove failed: %v", err)
		}
	}
	delete(c.Disks, name)
	return d, deleteFile, nil
}

// --- helpers ---

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// validateSize ensures a size string looks reasonable (non-empty).
func validateSize(size string) error {
	if _, err := parseSizeGB(size); err != nil {
		return err
	}
	return nil
}
