package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// --- create-vm ---

const baseSSHPort = 2222
const baseSPICEPort = 5930

// CreateVMOptions holds optional parameters for create-vm. These are persisted
// on the VM so that `run` can be invoked with no flags.
type CreateVMOptions struct {
	Disk    string   // override the "last disk created" default
	RAM     string   // override default 8G
	Arch    string   // override default x86_64
	CPUs    int      // -smp count (0 == qemu default)
	Display string   // qemu -display type ("" == windowed default, "none" == headless)
	Spice   bool     // enable SPICE + guest agent channel (clipboard, etc.)
	ISO     string   // default ISO to boot/install from (absolute path)
	Extra   []string // default extra args passed verbatim to qemu
}

// CreateVM registers a new VM config, auto-assigning an SSH port and defaulting
// the disk to the most recently created disk.
func CreateVM(r Runner, c *Config, name string, opts CreateVMOptions) (*VM, error) {
	if name == "" {
		return nil, errors.New("vm name is required")
	}
	if _, ok := c.VMs[name]; ok {
		return nil, fmt.Errorf("vm %q already exists", name)
	}
	diskName := opts.Disk
	if diskName == "" {
		last := c.lastDisk()
		if last == nil {
			return nil, errors.New("no disk exists; create a disk first")
		}
		diskName = last.Name
	}
	if _, err := getDisk(c, diskName); err != nil {
		return nil, err
	}
	ram := opts.RAM
	if ram == "" {
		ram = "8G"
	}
	if err := validateSize(ram); err != nil {
		return nil, err
	}
	arch := opts.Arch
	if arch == "" {
		arch = "x86_64"
	}
	if opts.CPUs < 0 {
		return nil, fmt.Errorf("cpus must be >= 0, got %d", opts.CPUs)
	}
	port := c.nextSSHPort(baseSSHPort)
	spicePort := 0
	if opts.Spice {
		spicePort = c.nextSPICEPort(baseSPICEPort)
	}

	vm := &VM{
		Name:      name,
		Disk:      diskName,
		RAM:       ram,
		Arch:      arch,
		CPUs:      opts.CPUs,
		Display:   opts.Display,
		Spice:     opts.Spice,
		SpicePort: spicePort,
		ISO:       opts.ISO,
		ExtraArgs: opts.Extra,
		SSHPort:   port,
		Created:   nowNanos(),
	}
	c.VMs[name] = vm
	return vm, nil
}

// --- set-vm ---

// VMUpdate describes a partial change to a VM's stored parameters. A nil pointer
// means "leave unchanged"; a non-nil pointer sets the field (an ISO pointer to
// "" clears the stored ISO).
type VMUpdate struct {
	Disk    *string
	RAM     *string
	Arch    *string
	CPUs    *int
	Display *string
	Spice   *bool
	ISO     *string
	Extra   *[]string
}

// IsEmpty reports whether the update would change nothing.
func (u VMUpdate) IsEmpty() bool {
	return u.Disk == nil && u.RAM == nil && u.Arch == nil && u.CPUs == nil &&
		u.Display == nil && u.Spice == nil && u.ISO == nil && u.Extra == nil
}

// SetVM applies a partial update to an existing VM, validating any referenced
// disk, RAM size and cpu count. Changes to a running VM take effect on its next
// run; SetVM never touches the live qemu process.
func SetVM(c *Config, name string, u VMUpdate) (*VM, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return nil, err
	}
	if u.IsEmpty() {
		return nil, errors.New("no parameters to change")
	}
	if u.Disk != nil {
		if _, err := getDisk(c, *u.Disk); err != nil {
			return nil, err
		}
		vm.Disk = *u.Disk
	}
	if u.RAM != nil {
		if err := validateSize(*u.RAM); err != nil {
			return nil, err
		}
		vm.RAM = *u.RAM
	}
	if u.Arch != nil {
		if *u.Arch == "" {
			return nil, errors.New("arch cannot be empty")
		}
		vm.Arch = *u.Arch
	}
	if u.CPUs != nil {
		if *u.CPUs < 0 {
			return nil, fmt.Errorf("cpus must be >= 0, got %d", *u.CPUs)
		}
		vm.CPUs = *u.CPUs
	}
	if u.Display != nil {
		vm.Display = *u.Display
	}
	if u.Spice != nil {
		if *u.Spice {
			if !vm.Spice || vm.SpicePort == 0 {
				vm.SpicePort = c.nextSPICEPort(baseSPICEPort)
			}
			vm.Spice = true
		} else {
			vm.Spice = false
			vm.SpicePort = 0
		}
	}
	if u.ISO != nil {
		vm.ISO = *u.ISO
	}
	if u.Extra != nil {
		vm.ExtraArgs = *u.Extra
	}
	return vm, nil
}

// --- run ---

// RunOptions holds per-run parameters. Any field left at its zero value falls
// back to the value persisted on the VM (see mergeRunOptions).
type RunOptions struct {
	ISO       string   // install/live ISO to boot from
	Display   string   // qemu -display type override
	CPUs      int      // -smp override
	Spice     bool     // enable SPICE channel for this run
	SpicePort int      // SPICE TCP port when Spice is enabled
	Accel     string   // qemu -machine accel value (e.g. "kvm:tcg"); set from arch
	Extra     []string // extra args appended verbatim to qemu (after "--")
	PidFile   string   // where qemu should write its pid (set by RunVM)
}

// defaultAccel returns the qemu accelerator list for an arch. On x86 we prefer
// hardware KVM for performance but fall back to software emulation (tcg) so VMs
// still start on hosts without /dev/kvm. Other arches use qemu's default.
func defaultAccel(arch string) string {
	switch arch {
	case "x86_64", "i386", "x86":
		return "kvm:tcg"
	default:
		return ""
	}
}

// mergeRunOptions resolves the effective run parameters by layering the per-run
// overrides on top of the values stored on the VM. Run-time extra args are
// appended after the VM's stored extra args.
func mergeRunOptions(vm *VM, o RunOptions) RunOptions {
	eff := RunOptions{
		ISO:       vm.ISO,
		Display:   vm.Display,
		CPUs:      vm.CPUs,
		Spice:     vm.Spice,
		SpicePort: vm.SpicePort,
		Accel:     defaultAccel(vm.Arch),
	}
	eff.Extra = append(eff.Extra, vm.ExtraArgs...)
	if o.ISO != "" {
		eff.ISO = o.ISO
	}
	if o.Display != "" {
		eff.Display = o.Display
	}
	if o.CPUs > 0 {
		eff.CPUs = o.CPUs
	}
	eff.Extra = append(eff.Extra, o.Extra...)
	return eff
}

// qemuArchBin returns the qemu binary name for an arch.
func qemuArchBin(arch string) string {
	return "qemu-system-" + arch
}

// qemuArgs builds the argument list to start a VM. hostFwdPort forwards to guest :22.
// If opts.ISO is set, the VM boots from the ISO first (then the disk), which is
// how a fresh/empty disk gets an OS installed. opts.Extra is appended verbatim.
func qemuArgs(diskPath, ram string, hostFwdPort int, opts RunOptions) []string {
	args := []string{
		"-m", ram,
		"-drive", "file=" + diskPath + ",format=qcow2",
		"-netdev", "user,id=net0,hostfwd=tcp::" + strconv.Itoa(hostFwdPort) + "-:22",
		"-device", "virtio-net-pci,netdev=net0",
	}
	// Hardware acceleration (KVM on x86) with graceful fallback to emulation.
	if opts.Accel != "" {
		args = append(args, "-machine", "accel="+opts.Accel)
	}
	if opts.CPUs > 0 {
		args = append(args, "-smp", strconv.Itoa(opts.CPUs))
	}
	if opts.Spice && opts.SpicePort > 0 {
		// SPICE + vdagent channel enables clipboard sharing and other guest-agent features.
		args = append(args,
			"-spice", "port="+strconv.Itoa(opts.SpicePort)+",disable-ticketing=on,agent-mouse=on",
			"-device", "virtio-serial-pci",
			"-chardev", "spicevmc,id=vdagent,name=vdagent",
			"-device", "virtserialport,chardev=vdagent,name=com.redhat.spice.0",
		)
	}
	// Display: an empty type lets qemu open its default graphical window (UI);
	// "none" runs headless. Only emit -display when a type is set so the default
	// stays windowed.
	if opts.Display != "" {
		args = append(args, "-display", opts.Display)
	}
	if opts.ISO != "" {
		// Attach the ISO as a CD-ROM and prefer booting from it (d), then the
		// disk (c). This lets an empty disk boot an installer/live image.
		args = append(args, "-cdrom", opts.ISO, "-boot", "order=dc")
	}
	if opts.PidFile != "" {
		args = append(args, "-pidfile", opts.PidFile)
	}
	args = append(args, "-daemonize") // detach so we can capture the pid
	args = append(args, opts.Extra...)
	return args
}

// vmPidFile returns the path qemu writes its pid to for a VM. It lives next to
// the disk image so it is easy to find and clean up.
func vmPidFile(diskPath, name string) string {
	return filepath.Join(filepath.Dir(diskPath), "."+name+".qemu.pid")
}

// RunVM starts the qemu process for a VM, after running-state and frozen checks.
// It stores the qemu pid in the config.
func RunVM(r Runner, c *Config, name string, opts RunOptions) (string, int, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return "", 0, err
	}
	// Already running? Check the stored pid.
	if vm.PID != 0 && pidIsQemu(vm.PID) {
		return "", vm.PID, fmt.Errorf("vm %q is already running (pid %d)", name, vm.PID)
	}
	d, err := getDisk(c, vm.Disk)
	if err != nil {
		return "", 0, err
	}
	if d.Frozen {
		return "", 0, fmt.Errorf("disk %q is frozen; cannot run vm %q", vm.Disk, name)
	}
	if vm.Spice && vm.SpicePort == 0 {
		vm.SpicePort = c.nextSPICEPort(baseSPICEPort)
	}

	opts = mergeRunOptions(vm, opts)
	opts.PidFile = vmPidFile(d.Path, name)
	// A stale pidfile from a previous run would confuse readPidFile; drop it.
	_ = os.Remove(opts.PidFile)

	args := qemuArgs(d.Path, vm.RAM, vm.SSHPort, opts)
	bin := qemuArchBin(vm.Arch)
	// Use -daemonize + -pidfile: qemu writes its pid to the pidfile once it has
	// successfully detached.
	res := r.Run(bin, args...)
	if res.Err != nil {
		return "", 0, fmt.Errorf("qemu failed: %v", res.Err)
	}
	// Prefer the pidfile (real qemu); fall back to stdout for the test runner.
	pid := readPidFile(opts.PidFile)
	if pid == 0 {
		pid = parseDaemonizePid(res.Stdout)
	}
	if pid == 0 {
		return bin, 0, fmt.Errorf("qemu reported no pid (pidfile %s); the VM may have failed to boot", opts.PidFile)
	}
	vm.PID = pid
	return bin, pid, nil
}

// readPidFile reads a pid written by qemu's -pidfile, returning 0 if absent or invalid.
func readPidFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// parseDaemonizePid extracts the pid qemu prints when -daemonize is used.
func parseDaemonizePid(out string) int {
	for _, line := range splitLines(out) {
		// qemu -daemonize prints nothing reliably on all builds, so also accept
		// an explicit numeric pid that our fake runner uses.
		if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// --- kill ---

// KillVM sends SIGKILL to the qemu process backing a VM, after verifying it is qemu.
func KillVM(c *Config, name string) (int, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return 0, err
	}
	if vm.PID == 0 {
		return 0, fmt.Errorf("vm %q is not running (no pid)", name)
	}
	pid := vm.PID
	if !pidIsQemu(pid) {
		// Stale pid; clear it so future runs work.
		vm.PID = 0
		return 0, fmt.Errorf("pid %d for vm %q is not a qemu process; cleared stale pid", pid, name)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, fmt.Errorf("find process: %v", err)
	}
	if err := proc.Kill(); err != nil {
		return 0, fmt.Errorf("kill failed: %v", err)
	}
	vm.PID = 0
	return pid, nil
}

// --- clear ---

// ClearVM checks whether the VM's stored pid is still alive; if not, clears it.
func ClearVM(c *Config, name string) (bool, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return false, err
	}
	if vm.PID == 0 {
		return true, fmt.Errorf("vm %q is not marked as running", name)
	}
	if pidAlive(vm.PID) {
		return false, fmt.Errorf("vm %q is still running (pid %d)", name, vm.PID)
	}
	vm.PID = 0
	return true, nil
}

// --- delete vm ---

// DeleteVM kills the VM if needed (confirmation), then removes the config entry.
func DeleteVM(c *Config, cf Confirmer, name string) (*VM, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return nil, err
	}
	if err := requireConfirmation(cf, fmt.Sprintf("Delete vm %q?", name)); err != nil {
		return vm, err
	}
	// Kill if running.
	if vm.PID != 0 && pidIsQemu(vm.PID) {
		_, _ = KillVM(c, name)
	}
	delete(c.VMs, name)
	return vm, nil
}

// --- ssh ---

// GetSSH returns the ssh command string for a VM.
func GetSSH(c *Config, name string) (string, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ssh -p %d root@127.0.0.1", vm.SSHPort), nil
}

// GetSpice returns the remote-viewer command string for a SPICE-enabled VM.
func GetSpice(c *Config, name string) (string, error) {
	vm, err := getVM(c, name)
	if err != nil {
		return "", err
	}
	if !vm.Spice || vm.SpicePort <= 0 {
		return "", fmt.Errorf("vm %q does not have SPICE enabled", name)
	}
	return fmt.Sprintf("remote-viewer spice://127.0.0.1:%d", vm.SpicePort), nil
}

// --- helpers ---

func getVM(c *Config, name string) (*VM, error) {
	v, ok := c.VMs[name]
	if !ok {
		return nil, fmt.Errorf("vm %q does not exist", name)
	}
	return v, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
