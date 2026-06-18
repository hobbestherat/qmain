package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Disk describes a qcow2 disk image known to qmain.
type Disk struct {
	Name    string `json:"name"`
	Path    string `json:"path"`              // absolute path to the .qcow2 file
	Size    string `json:"size,omitempty"`    // requested size, e.g. "100G"
	Frozen  bool   `json:"frozen"`            // read-only / backing-protected
	Base    string `json:"base,omitempty"`    // name of the base disk if this is a clone
	Created int64  `json:"created,omitempty"` // creation time, unix nanos (also used for ordering)
}

// VM describes a named virtual machine configuration.
type VM struct {
	Name      string   `json:"name"`
	Disk      string   `json:"disk"`                // disk name this VM boots from
	RAM       string   `json:"ram,omitempty"`       // e.g. "8G"
	Arch      string   `json:"arch,omitempty"`      // qemu architecture, e.g. "x86_64"
	CPUs      int      `json:"cpus,omitempty"`      // -smp count (0 == qemu default)
	Display   string   `json:"display,omitempty"`   // qemu -display type; "" == windowed default, "none" == headless
	ISO       string   `json:"iso,omitempty"`       // default install/boot ISO (absolute path)
	ExtraArgs []string `json:"extraArgs,omitempty"` // default extra args passed verbatim to qemu
	SSHPort   int      `json:"sshPort"`             // host port forwarded to guest :22
	PID       int      `json:"pid,omitempty"`       // last known qemu pid (0 == not running)
	Created   int64    `json:"created,omitempty"`
}

// Config is the on-disk state of all known disks and VMs.
type Config struct {
	Disks map[string]*Disk `json:"disks"`
	VMs   map[string]*VM   `json:"vms"`
}

func newConfig() *Config {
	return &Config{Disks: map[string]*Disk{}, VMs: map[string]*VM{}}
}

// lastDisk returns the most recently created disk, or nil if there are none.
func (c *Config) lastDisk() *Disk {
	var best *Disk
	for _, d := range c.Disks {
		if best == nil || d.Created > best.Created {
			best = d
		}
	}
	return best
}

// nextSSHPort returns the smallest port >= basePort not used by any VM.
func (c *Config) nextSSHPort(basePort int) int {
	used := map[int]bool{}
	for _, vm := range c.VMs {
		used[vm.SSHPort] = true
	}
	for p := basePort; ; p++ {
		if !used[p] {
			return p
		}
	}
}

// disksWithBase lists disks whose Base == name.
func (c *Config) disksWithBase(name string) []*Disk {
	var out []*Disk
	for _, d := range c.Disks {
		if d.Base == name {
			out = append(out, d)
		}
	}
	return out
}

// vmsUsingDisk lists VMs whose Disk == name.
func (c *Config) vmsUsingDisk(name string) []*VM {
	var out []*VM
	for _, v := range c.VMs {
		if v.Disk == name {
			out = append(out, v)
		}
	}
	return out
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".qmain", "config.json")
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newConfig(), nil
		}
		return nil, err
	}
	c := newConfig()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	if c.Disks == nil {
		c.Disks = map[string]*Disk{}
	}
	if c.VMs == nil {
		c.VMs = map[string]*VM{}
	}
	return c, nil
}

func saveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// nowNanos is overridable in tests; default returns the current time.
var nowNanos = func() int64 { return time.Now().UnixNano() }
