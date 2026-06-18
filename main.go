package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PreviewRunner is a no-op runner used in --preview mode. Commands are never
// executed; the CLI layer prints what would run before calling it.
type PreviewRunner struct{}

func (PreviewRunner) Run(name string, args ...string) ExecResult {
	return ExecResult{Cmd: name + " " + strings.Join(args, " ")}
}

// App wires together the config path, runner and confirmer.
type App struct {
	ConfigPath string
	Runner     Runner
	Confirmer  Confirmer
	Preview    bool
}

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}

	// Global flags (--alt-config, --preview) can be placed before the command.
	var (
		altConfig string
		preview   bool
	)
	rest := []string{}
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--preview":
			preview = true
			i++
		case strings.HasPrefix(a, "--alt-config="):
			altConfig = a[len("--alt-config="):]
			i++
		case a == "--alt-config":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --alt-config requires an argument")
				return 2
			}
			altConfig = args[i]
			i++
		default:
			// First non-global token is the command; everything after it
			// (including any "-- <extra>" passthrough) is left untouched.
			rest = args[i:]
			i = len(args)
		}
	}

	if len(rest) == 0 {
		usage()
		return 2
	}

	cfgPath := defaultConfigPath()
	if altConfig != "" {
		cfgPath = altConfig
	}

	app := &App{
		ConfigPath: cfgPath,
		Runner:     ShellRunner{},
		Preview:    preview,
	}
	if preview {
		app.Runner = PreviewRunner{}
		app.Confirmer = PreviewConfirmer{}
	} else {
		app.Confirmer = TerminalConfirmer{}
	}

	code, err := app.Dispatch(rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return code
	}
	return code
}

func usage() {
	fmt.Fprintln(os.Stderr, `qmain - lightweight QEMU VM/disk manager

Global flags (before the command):
  --alt-config <file>   use <file> instead of ~/.qmain/config.json
  --preview             print the command/question/failure instead of running

Commands:
  create-disk <name> <size>
  create-vm <name> [--disk <name>] [--ram <size>] [--cpu <arch>] [--cpus <n>] [--display <type>] [--iso <file>] [-- <extra qemu args>]
  clone-disk <name> --base <base-name>
  freeze-disk <name>
  unfreeze-disk <name>
  delete-disk <name>
  set-vm <name> [--disk <name>] [--ram <size>] [--cpu <arch>] [--cpus <n>] [--display <type>] [--iso <file>] [--no-iso] [-- <extra qemu args>]
  run <vm-name> [--iso <file>] [--display <type>] [--cpus <n>] [-- <extra qemu args>]
  kill <vm-name>
  clear <vm-name>
  get-ssh <vm-name>
  delete <vm-name>
  list
  list-disks`)
}

// Dispatch routes a parsed command (no global flags) to its handler.
func (a *App) Dispatch(cmd []string) (int, error) {
	if len(cmd) == 0 {
		usage()
		return 2, fmt.Errorf("no command")
	}
	switch cmd[0] {
	case "create-disk":
		return a.cmdCreateDisk(cmd[1:])
	case "create-vm":
		return a.cmdCreateVM(cmd[1:])
	case "set-vm":
		return a.cmdSetVM(cmd[1:])
	case "clone-disk":
		return a.cmdCloneDisk(cmd[1:])
	case "freeze-disk":
		return a.cmdFreezeDisk(cmd[1:])
	case "unfreeze-disk":
		return a.cmdUnfreezeDisk(cmd[1:])
	case "delete-disk":
		return a.cmdDeleteDisk(cmd[1:])
	case "run":
		return a.cmdRun(cmd[1:])
	case "kill":
		return a.cmdKill(cmd[1:])
	case "list":
		return a.cmdList(cmd[1:])
	case "list-disks":
		return a.cmdListDisks(cmd[1:])
	case "clear":
		return a.cmdClear(cmd[1:])
	case "get-ssh":
		return a.cmdGetSSH(cmd[1:])
	case "delete":
		return a.cmdDelete(cmd[1:])
	default:
		usage()
		return 2, fmt.Errorf("unknown command %q", cmd[0])
	}
}

// load loads the config and returns it.
func (a *App) load() (*Config, error) {
	c, err := loadConfig(a.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return c, nil
}

// commit saves the config if not in preview mode.
func (a *App) commit(c *Config) error {
	if a.Preview {
		return nil
	}
	return saveConfig(a.ConfigPath, c)
}

// previewError surfaces a computed failure: under --preview it prints the error
// (so the command stays observable without side effects), and it always returns
// the standard failing (1, err) result for the dispatcher.
func (a *App) previewError(err error) (int, error) {
	if a.Preview {
		fmt.Printf("[preview] error: %v\n", err)
	}
	return 1, err
}

// --- create-disk ---

func (a *App) cmdCreateDisk(args []string) (int, error) {
	if len(args) < 2 {
		return 2, fmt.Errorf("usage: create-disk <name> <size>")
	}
	name, size := args[0], args[1]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if a.Preview {
		previewln("qemu-img create -f qcow2 %s %s", diskPath(name), size)
		return 0, nil
	}
	d, err := CreateDisk(a.Runner, c, name, size)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Created disk %q at %s (%s)\n", d.Name, d.Path, d.Size)
	return 0, nil
}

// --- create-vm ---

func (a *App) cmdCreateVM(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: create-vm <name> [--disk <name>] [--ram <size>] [--cpu <arch>] [--cpus <n>] [--display <type>] [--iso <file>] [-- <extra qemu args>]")
	}
	name := args[0]
	u, err := parseVMUpdate(args[1:])
	if err != nil {
		return 2, err
	}
	opts := optionsFromUpdate(u)
	// Persist the ISO as an absolute path so `run` works from any directory.
	if opts.ISO != "" {
		if abs, aerr := filepath.Abs(opts.ISO); aerr == nil {
			opts.ISO = abs
		}
	}
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	ram := opts.RAM
	if ram == "" {
		ram = "8G"
	}
	arch := opts.Arch
	if arch == "" {
		arch = "x86_64"
	}
	if a.Preview {
		diskName := opts.Disk
		if diskName == "" {
			last := c.lastDisk()
			if last != nil {
				diskName = last.Name
			}
		}
		port := c.nextSSHPort(baseSSHPort)
		previewln("would register vm %q (disk=%s ram=%s arch=%s cpus=%s display=%s iso=%s ssh-port=%d)",
			name, diskName, ram, arch, cpusStr(opts.CPUs), displayStr(opts.Display), isoStr(opts.ISO), port)
		return 0, nil
	}
	vm, err := CreateVM(a.Runner, c, name, opts)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Created vm %q (disk=%s ram=%s arch=%s cpus=%s display=%s iso=%s ssh-port=%d)\n",
		vm.Name, vm.Disk, vm.RAM, vm.Arch, cpusStr(vm.CPUs), displayStr(vm.Display), isoStr(vm.ISO), vm.SSHPort)
	return 0, nil
}

// --- set-vm ---

func (a *App) cmdSetVM(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: set-vm <name> [--disk <name>] [--ram <size>] [--cpu <arch>] [--cpus <n>] [--display <type>] [--iso <file>] [--no-iso] [-- <extra qemu args>]")
	}
	name := args[0]
	u, err := parseVMUpdate(args[1:])
	if err != nil {
		return 2, err
	}
	// Resolve a newly-set ISO to an absolute path (skip when clearing via --no-iso).
	if u.ISO != nil && *u.ISO != "" {
		if abs, aerr := filepath.Abs(*u.ISO); aerr == nil {
			u.ISO = &abs
		}
	}
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	// SetVM mutates the in-memory VM; in preview mode commit() is a no-op, so the
	// change is computed and printed but never persisted.
	vm, serr := SetVM(c, name, u)
	if serr != nil {
		return a.previewError(serr)
	}
	if a.Preview {
		previewln("would update vm %q -> (disk=%s ram=%s arch=%s cpus=%s display=%s iso=%s)",
			name, vm.Disk, vm.RAM, vm.Arch, cpusStr(vm.CPUs), displayStr(vm.Display), isoStr(vm.ISO))
		return 0, nil
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Updated vm %q (disk=%s ram=%s arch=%s cpus=%s display=%s iso=%s)\n",
		vm.Name, vm.Disk, vm.RAM, vm.Arch, cpusStr(vm.CPUs), displayStr(vm.Display), isoStr(vm.ISO))
	if vm.PID != 0 && pidIsQemu(vm.PID) {
		fmt.Println("note: vm is running; changes take effect on next run")
	}
	return 0, nil
}

// --- clone-disk ---

func (a *App) cmdCloneDisk(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: clone-disk <name> --base <base-name>")
	}
	name := args[0]
	base := ""
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--base" && i+1 < len(args):
			base = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--base="):
			base = args[i][len("--base="):]
		default:
			return 2, fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if base == "" {
		return 2, fmt.Errorf("--base is required")
	}
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	bd, gerr := getDisk(c, base)
	if gerr != nil {
		return a.previewError(gerr)
	}
	newPath := diskPath(name)
	if !bd.Frozen {
		return a.previewError(fmt.Errorf("base disk %q is not frozen; freeze it first", base))
	}
	if !isBelowOrSamePath(filepath.Dir(bd.Path), newPath) {
		return a.previewError(fmt.Errorf("clone target %q is not in or below the base disk directory %q",
			filepath.Dir(newPath), filepath.Dir(bd.Path)))
	}
	if a.Preview {
		previewln("qemu-img create -f qcow2 -b %s -F qcow2 %s", bd.Path, newPath)
		return 0, nil
	}
	d, err := CloneDisk(a.Runner, c, name, base)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Cloned disk %q from %q at %s\n", d.Name, d.Base, d.Path)
	return 0, nil
}

// --- freeze-disk ---

func (a *App) cmdFreezeDisk(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: freeze-disk <name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if a.Preview {
		previewln("chmod 444 %s", diskPath(name))
		return 0, nil
	}
	d, err := FreezeDisk(a.Runner, c, name)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Froze disk %q\n", d.Name)
	return 0, nil
}

// --- unfreeze-disk ---

func (a *App) cmdUnfreezeDisk(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: unfreeze-disk <name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if a.Preview {
		deps := c.disksWithBase(name)
		if len(deps) > 0 {
			names := make([]string, len(deps))
			for i, dep := range deps {
				names[i] = dep.Name
			}
			fmt.Printf("[preview] error: cannot unfreeze %q: cloned disks depend on it: %s\n",
				name, joinNames(names))
			return 0, nil
		}
		previewln("chmod 644 %s", diskPath(name))
		return 0, nil
	}
	d, err := UnfreezeDisk(a.Runner, c, name)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Unfroze disk %q\n", d.Name)
	return 0, nil
}

// --- delete-disk ---

func (a *App) cmdDeleteDisk(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: delete-disk <name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if a.Preview {
		_, gerr := getDisk(c, name)
		if gerr != nil {
			fmt.Printf("[preview] error: %v\n", gerr)
			return 0, nil
		}
		if vms := c.vmsUsingDisk(name); len(vms) > 0 {
			names := make([]string, len(vms))
			for i, v := range vms {
				names[i] = v.Name
			}
			fmt.Printf("[preview] error: cannot delete %q: VMs depend on it: %s\n",
				name, joinNames(names))
			return 0, nil
		}
		if clones := c.disksWithBase(name); len(clones) > 0 {
			names := make([]string, len(clones))
			for i, cl := range clones {
				names[i] = cl.Name
			}
			fmt.Printf("[preview] error: cannot delete %q: cloned disks depend on it: %s\n",
				name, joinNames(names))
			return 0, nil
		}
		fmt.Printf("[preview] would ask: Delete disk %q? [y/N]\n", name)
		fmt.Printf("[preview] would run: rm %s\n", diskPath(name))
		return 0, nil
	}
	d, err := DeleteDisk(a.Runner, c, a.Confirmer, name)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Deleted disk %q\n", d.Name)
	return 0, nil
}

// --- run ---

func (a *App) cmdRun(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: run <vm-name> [--iso <file>] [--display <type>] [--cpus <n>] [-- <extra qemu args>]")
	}
	name := args[0]
	ropts, perr := parseRunFlags(args[1:])
	if perr != nil {
		return 2, perr
	}
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	vm, verr := getVM(c, name)
	if verr != nil {
		return a.previewError(verr)
	}
	d, derr := getDisk(c, vm.Disk)
	if derr != nil {
		return a.previewError(derr)
	}
	if vm.PID != 0 && pidIsQemu(vm.PID) {
		return a.previewError(fmt.Errorf("vm %q is already running (pid %d)", name, vm.PID))
	}
	if d.Frozen {
		return a.previewError(fmt.Errorf("disk %q is frozen; cannot run vm %q", vm.Disk, name))
	}
	// Resolve a run-time ISO override to an absolute path so qemu can find it
	// regardless of cwd. (A stored ISO is already absolute.)
	if ropts.ISO != "" {
		if abs, aerr := filepath.Abs(ropts.ISO); aerr == nil {
			ropts.ISO = abs
		}
	}
	// eff is the merged view (stored VM params + this run's overrides), used for
	// preview rendering and the ISO existence check. RunVM re-merges internally.
	eff := mergeRunOptions(vm, ropts)
	bin := qemuArchBin(vm.Arch)
	if a.Preview {
		eff.PidFile = vmPidFile(d.Path, name)
		args2 := qemuArgs(d.Path, vm.RAM, vm.SSHPort, eff)
		previewln("%s %s", bin, strings.Join(args2, " "))
		return 0, nil
	}
	if eff.ISO != "" {
		if _, serr := os.Stat(eff.ISO); serr != nil {
			return 1, fmt.Errorf("iso %q not found: %v", eff.ISO, serr)
		}
	}
	_, pid, err := RunVM(a.Runner, c, name, ropts)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Started vm %q (pid %d, ssh: %s)\n", name, pid, sshCmd(vm.SSHPort))
	return 0, nil
}

// parseRunFlags parses the optional flags for `run`: --iso <file>, --display
// <type>, --cpus <n>, and a "--" terminator after which all remaining args are
// passed verbatim to qemu. Any flag omitted falls back to the value stored on
// the VM at create time.
func parseRunFlags(args []string) (RunOptions, error) {
	var opts RunOptions
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			opts.Extra = append(opts.Extra, args[i+1:]...)
			return opts, nil
		case matchFlag(a, "--iso"):
			v, ni, err := flagVal(args, i, "--iso")
			if err != nil {
				return opts, err
			}
			opts.ISO, i = v, ni
		case matchFlag(a, "--display"):
			v, ni, err := flagVal(args, i, "--display")
			if err != nil {
				return opts, err
			}
			opts.Display, i = v, ni
		case matchFlag(a, "--cpus") || matchFlag(a, "--smp"):
			name := "--cpus"
			if matchFlag(a, "--smp") {
				name = "--smp"
			}
			v, ni, err := flagVal(args, i, name)
			if err != nil {
				return opts, err
			}
			n, perr := parseCPUs(v)
			if perr != nil {
				return opts, perr
			}
			opts.CPUs, i = n, ni
		default:
			return opts, fmt.Errorf("unexpected argument %q", a)
		}
	}
	return opts, nil
}

// --- kill ---

func (a *App) cmdKill(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: kill <vm-name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	vm, verr := getVM(c, name)
	if verr != nil {
		return a.previewError(verr)
	}
	if vm.PID == 0 {
		return a.previewError(fmt.Errorf("vm %q is not running (no pid)", name))
	}
	if !pidIsQemu(vm.PID) {
		msg := fmt.Sprintf("pid %d for vm %q is not a qemu process; would clear stale pid", vm.PID, name)
		if a.Preview {
			fmt.Printf("[preview] %s\n", msg)
			return 0, nil
		}
		vm.PID = 0
		if err := a.commit(c); err != nil {
			return 1, err
		}
		fmt.Println(msg)
		return 0, nil
	}
	if a.Preview {
		previewln("kill -9 %d", vm.PID)
		return 0, nil
	}
	pid, err := KillVM(c, name)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Killed vm %q (was pid %d)\n", name, pid)
	return 0, nil
}

// --- clear ---

func (a *App) cmdClear(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: clear <vm-name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	vm, verr := getVM(c, name)
	if verr != nil {
		return a.previewError(verr)
	}
	if vm.PID == 0 {
		return a.previewError(fmt.Errorf("vm %q is not marked as running", name))
	}
	if pidAlive(vm.PID) {
		return a.previewError(fmt.Errorf("vm %q is still running (pid %d)", name, vm.PID))
	}
	if a.Preview {
		previewln("would clear stale pid %d for vm %q", vm.PID, name)
		return 0, nil
	}
	_, err = ClearVM(c, name)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Cleared stale pid for vm %q\n", name)
	return 0, nil
}

// --- get-ssh ---

func (a *App) cmdGetSSH(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: get-ssh <vm-name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	cmd, err := GetSSH(c, name)
	if err != nil {
		return 1, err
	}
	fmt.Println(cmd)
	return 0, nil
}

// --- delete ---

func (a *App) cmdDelete(args []string) (int, error) {
	if len(args) < 1 {
		return 2, fmt.Errorf("usage: delete <vm-name>")
	}
	name := args[0]
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if a.Preview {
		_, gerr := getVM(c, name)
		if gerr != nil {
			fmt.Printf("[preview] error: %v\n", gerr)
			return 0, nil
		}
		fmt.Printf("[preview] would ask: Delete vm %q? [y/N]\n", name)
		vm, _ := getVM(c, name)
		if vm != nil && vm.PID != 0 && pidIsQemu(vm.PID) {
			fmt.Printf("[preview] would run: kill -9 %d\n", vm.PID)
		}
		fmt.Printf("[preview] would remove vm config entry\n")
		return 0, nil
	}
	vm, err := DeleteVM(c, a.Confirmer, name)
	if err != nil {
		return 1, err
	}
	if err := a.commit(c); err != nil {
		return 1, err
	}
	fmt.Printf("Deleted vm %q\n", vm.Name)
	return 0, nil
}

// --- list ---

func (a *App) cmdList(args []string) (int, error) {
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if len(c.VMs) == 0 {
		fmt.Println("No VMs.")
		return 0, nil
	}
	fmt.Printf("%-16s %-12s %-8s %-10s %-6s %-8s %-8s %-8s\n", "NAME", "DISK", "RAM", "ARCH", "CPUS", "DISPLAY", "SSH", "PID")
	for _, v := range c.VMs {
		pidStr := "-"
		if v.PID != 0 {
			pidStr = strconv.Itoa(v.PID)
		}
		fmt.Printf("%-16s %-12s %-8s %-10s %-6s %-8s %-8d %-8s\n",
			v.Name, v.Disk, v.RAM, v.Arch, cpusStr(v.CPUs), displayStr(v.Display), v.SSHPort, pidStr)
	}
	return 0, nil
}

func (a *App) cmdListDisks(args []string) (int, error) {
	c, err := a.load()
	if err != nil {
		return 1, err
	}
	if len(c.Disks) == 0 {
		fmt.Println("No disks.")
		return 0, nil
	}
	fmt.Printf("%-16s %-8s %-8s %-10s %s\n", "NAME", "SIZE", "FROZEN", "BASE", "PATH")
	for _, d := range c.Disks {
		frozen := "no"
		if d.Frozen {
			frozen = "yes"
		}
		base := "-"
		if d.Base != "" {
			base = d.Base
		}
		fmt.Printf("%-16s %-8s %-8s %-10s %s\n", d.Name, d.Size, frozen, base, d.Path)
	}
	return 0, nil
}

// --- helpers ---

// matchFlag reports whether token a is "--name" or "--name=...".
func matchFlag(a, name string) bool {
	return a == name || strings.HasPrefix(a, name+"=")
}

// flagVal extracts the value for the flag at args[i] (already matched by name),
// supporting both "--name value" and "--name=value". It returns the value and
// the next index to continue parsing from.
func flagVal(args []string, i int, name string) (string, int, error) {
	a := args[i]
	if strings.HasPrefix(a, name+"=") {
		return a[len(name)+1:], i + 1, nil
	}
	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s requires a value", name)
	}
	return args[i+1], i + 2, nil
}

// parseCPUs parses a -smp count (non-negative; 0 means "qemu default").
func parseCPUs(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cpus value %q (want a non-negative integer)", s)
	}
	return n, nil
}

// parseVMUpdate parses the VM-parameter flags shared by create-vm and set-vm.
// Unset flags yield nil pointers so callers can distinguish "not provided" from
// "set to zero/empty". A "--" terminator captures the remaining args as extra
// qemu args; "--no-iso" clears a stored ISO.
func parseVMUpdate(args []string) (VMUpdate, error) {
	var u VMUpdate
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			extra := append([]string{}, args[i+1:]...)
			u.Extra = &extra
			return u, nil
		case a == "--no-iso":
			empty := ""
			u.ISO = &empty
			i++
		case matchFlag(a, "--disk"):
			v, ni, err := flagVal(args, i, "--disk")
			if err != nil {
				return u, err
			}
			u.Disk, i = &v, ni
		case matchFlag(a, "--ram"):
			v, ni, err := flagVal(args, i, "--ram")
			if err != nil {
				return u, err
			}
			u.RAM, i = &v, ni
		case matchFlag(a, "--cpu"):
			v, ni, err := flagVal(args, i, "--cpu")
			if err != nil {
				return u, err
			}
			u.Arch, i = &v, ni
		case matchFlag(a, "--cpus") || matchFlag(a, "--smp"):
			name := "--cpus"
			if matchFlag(a, "--smp") {
				name = "--smp"
			}
			v, ni, err := flagVal(args, i, name)
			if err != nil {
				return u, err
			}
			n, perr := parseCPUs(v)
			if perr != nil {
				return u, perr
			}
			u.CPUs, i = &n, ni
		case matchFlag(a, "--display"):
			v, ni, err := flagVal(args, i, "--display")
			if err != nil {
				return u, err
			}
			u.Display, i = &v, ni
		case matchFlag(a, "--iso"):
			v, ni, err := flagVal(args, i, "--iso")
			if err != nil {
				return u, err
			}
			u.ISO, i = &v, ni
		default:
			return u, fmt.Errorf("unexpected argument %q", a)
		}
	}
	return u, nil
}

// optionsFromUpdate converts a parsed VMUpdate into CreateVMOptions for
// create-vm; CreateVM fills in defaults for any zero values.
func optionsFromUpdate(u VMUpdate) CreateVMOptions {
	var o CreateVMOptions
	if u.Disk != nil {
		o.Disk = *u.Disk
	}
	if u.RAM != nil {
		o.RAM = *u.RAM
	}
	if u.Arch != nil {
		o.Arch = *u.Arch
	}
	if u.CPUs != nil {
		o.CPUs = *u.CPUs
	}
	if u.Display != nil {
		o.Display = *u.Display
	}
	if u.ISO != nil {
		o.ISO = *u.ISO
	}
	if u.Extra != nil {
		o.Extra = *u.Extra
	}
	return o
}

func previewln(format string, args ...interface{}) {
	fmt.Printf("[preview] %s\n", fmt.Sprintf(format, args...))
}

func sshCmd(port int) string { return fmt.Sprintf("ssh -p %d root@127.0.0.1", port) }

// cpusStr renders a -smp count for display ("default" when unset).
func cpusStr(n int) string {
	if n <= 0 {
		return "default"
	}
	return strconv.Itoa(n)
}

// displayStr renders a display type for output ("window" when unset).
func displayStr(d string) string {
	if d == "" {
		return "window"
	}
	return d
}

// isoStr renders an ISO path for output ("-" when unset).
func isoStr(p string) string {
	if p == "" {
		return "-"
	}
	return p
}
