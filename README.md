# qmain

A lightweight command-line manager for QEMU virtual machines and qcow2 disk
images. It keeps all known disks and VMs in a single JSON config file
(`~/.qmain/config.json` by default) and shells out to `qemu-img` /
`qemu-system-*` to do the real work.

## Quickstart

```sh
go build -o qmain .

# 1. Create a disk and a VM. The ISO is only needed to install, so pass it at run.
qmain create-disk myvm 40G
qmain create-vm   myvm --cpus 4 --ram 8G
qmain run         myvm --iso ./ubuntu-24.04.iso   # one-off: boot the installer, install to disk

# 2. Once installed, the ISO is no longer needed — just run it.
qmain run myvm                         # boots straight from the disk
qmain get-ssh myvm                     # ssh -p 2222 root@127.0.0.1
qmain list
qmain kill myvm
```

`--iso` on `run` is a **temporary, per-run** parameter: it attaches the install
media for that one boot and is never stored on the VM. After the OS is installed
onto the disk, every later run (and every clone) boots from the disk directly.

Try anything safely with `--preview` (prints the exact command without running it):

```sh
qmain --preview run myvm
```

## Fast clones (copy-on-write)

The headline feature: spin up a new disk from a "golden" image **instantly**,
without copying gigabytes. A clone is a thin qcow2 overlay whose backing file is
the base image — only the *changes* are stored. Cloning a 40 GB base creates a
~200 KB file, so each clone is a clean, freshly-installed system that comes up in
seconds.

```sh
# Prepare a golden image once: install an OS from the ISO, then freeze it.
qmain create-disk golden 40G
qmain create-vm   golden
qmain run         golden --iso ./ubuntu-24.04.iso   # one-off install, then shut down
qmain freeze-disk golden                            # read-only so clones stay valid

# The ISO is done. Clones boot the already-installed system — no ISO needed.
qmain clone-disk  web1 --base golden
qmain clone-disk  web2 --base golden
qmain create-vm   web1 --disk web1
qmain create-vm   web2 --disk web2
qmain run web1     # clean, freshly-installed system in seconds
qmain run web2
```

Each clone boots from its own copy-on-write overlay, so the VMs are fully
independent while sharing the unchanged base blocks on disk.

## Changing VM parameters

Store the important launch parameters once, then change them later with
`set-vm` — only the flags you pass are modified:

```sh
qmain set-vm myvm --cpus 8 --ram 16G           # bump resources
qmain set-vm myvm --display none               # run headless from now on
qmain set-vm myvm --iso ./other.iso            # change boot media
qmain set-vm myvm --no-iso                     # stop attaching an ISO
qmain set-vm myvm -- -enable-kvm -cpu host     # replace the stored extra args
```

## Command reference

Disk images are created in the current working directory. `--disk` defaults to
the most recently created disk.

```
# Disks
qmain create-disk  <name> <size>                    # qemu-img create -f qcow2 <name>.qcow2 <size>
qmain clone-disk    <name> --base <base-name>       # instant copy-on-write clone; base must be frozen
qmain freeze-disk   <name>                          # chmod 444 + mark frozen (required before cloning)
qmain unfreeze-disk <name>                          # refuses if clones depend on it, listing them
qmain delete-disk   <name>                          # refuses if VMs or clones depend on it; asks confirmation

# VMs
qmain create-vm <name> [--disk <name>] [--ram <8G>] [--cpu <x86_64>] [--cpus <n>] [--display <type>] [--spice] [--iso <file>] [-- <extra qemu args>]
qmain set-vm    <name> [--disk <name>] [--ram <size>] [--cpu <arch>] [--cpus <n>] [--display <type>] [--spice|--no-spice] [--iso <file>] [--no-iso] [-- <extra qemu args>]
qmain run       <name> [--iso <file>] [--display <type>] [--cpus <n>] [-- <extra qemu args>]   # flags override stored params
qmain kill      <name>                              # verifies pid is qemu first, then SIGKILL
qmain clear     <name>                              # clears stale pid if the process is dead
qmain get-ssh   <name>                              # prints "ssh -p <port> root@127.0.0.1"
qmain get-spice <name>                              # prints "remote-viewer spice://127.0.0.1:<port>"
qmain delete    <name>                              # asks confirmation, kills if running, removes config
qmain list                                          # list VMs
qmain list-disks                                    # list disks

# Aliases
qmain rm <name>                                     # alias for delete
qmain ls                                            # alias for list
qmain rmd <name>                                    # alias for delete-disk
qmain lsd                                           # alias for list-disks
```

Global flags (placed **before** the command):

```
--alt-config <file>   use <file> instead of ~/.qmain/config.json
--preview             print the command/question/failure, but don't run or wait
```

## Persistent VM parameters

The important launch parameters are stored **on the VM** (at `create-vm` time,
or later via `set-vm`), so day-to-day use is just `qmain run <vm>`:

| Parameter   | flag                | qemu effect                                            |
|-------------|---------------------|--------------------------------------------------------|
| CPUs        | `--cpus <n>`        | `-smp <n>` (omitted when unset)                        |
| Display     | `--display <type>`  | `-display <type>`; unset = a normal **UI window**, `none` = headless |
| SPICE       | `--spice`           | enables SPICE/vdagent channel (clipboard etc.) and tracks an allocated `spicePort` |
| ISO         | `--iso <file>`      | `-cdrom <file> -boot order=dc`                         |
| Extra args  | `-- <args...>`      | appended verbatim to `qemu-system-*`                   |

Any flag passed to `run` overrides the stored value for that single run (and
run-time `-- <args>` are appended after the stored extra args):

```sh
qmain run dev --display none --cpus 8 -- -snapshot   # headless, 8 cpus, throwaway disk
```

The ISO is the typical example: you normally only need it once, to install — so
pass it as `run --iso <file>` (a temporary override) rather than storing it. The
`create-vm --iso` form exists for VMs that should always boot the same media.

`run` launches qemu with `-daemonize -pidfile <dir>/.<vm>.qemu.pid` and records
the pid from that file, so `kill`, `clear`, `list` and `delete` can reliably
track the running VM.

> **Display note:** a fresh VM opens qemu's default graphical window. A windowed
> display needs a desktop session (a valid `DISPLAY`/Wayland); on a headless host
> use `--display none` and reach the guest over the forwarded SSH port.

> **Acceleration:** on x86 hosts VMs run with KVM by default
> (`-machine accel=kvm:tcg`). When `/dev/kvm` is unavailable qemu falls back to
> software emulation automatically, so VMs still start everywhere. Other arches
> use qemu's default accelerator.

## Deleting a disk

`delete-disk` asks **two** questions so you don't lose data by accident:

1. *Remove disk `<name>` from qmain?* — drops it from qmain's config.
2. *Also delete the image file `<path>`?* — answer **no** to keep the `.qcow2`
   file on disk and only detach it from qmain; answer **yes** to also `rm` it.

It still refuses outright while a VM or a clone depends on the disk.

## Clone safety

- `clone-disk` requires the base to be **frozen** (refuses otherwise), so the
  shared backing file can never change underneath a clone.
- The new disk must be in the same directory as, or a subdirectory of, the base
  disk's directory; otherwise it is refused. This keeps relative backing-file
  references portable.
- `unfreeze-disk` and `delete-disk` refuse while cloned disks depend on the
  base, and list the dependent clones.

## Design

The code is split into small, testable layers:

| File         | Responsibility                                                       |
|--------------|----------------------------------------------------------------------|
| `config.go`  | `Config`, `Disk`, `VM` structs + JSON load/save and lookups.         |
| `runner.go`  | `Runner` interface that abstracts `exec.Command`.                    |
| `util.go`    | `Confirmer` (yes/no prompting), size/path/pid helpers.               |
| `disk.go`    | Pure logic for `create/clone/freeze/unfreeze/delete-disk`.           |
| `vm.go`      | Pure logic for `create-vm/set-vm/run/kill/clear/delete/get-ssh`.     |
| `main.go`    | CLI argument parsing, global flags, `--preview` rendering, dispatch.  |

Key design decisions:

- **Logic is separate from I/O.** Each operation is a function that takes a
  `*Config` (and a `Runner`/`Confirmer` for the parts that touch the world),
  mutates the config in memory and returns an error. The CLI layer loads the
  config, calls the function, and saves it.
- **`Runner` interface.** `ShellRunner` runs real commands; tests inject a
  `fakeRunner` that records calls and returns canned output.
- **`Confirmer` interface.** `TerminalConfirmer` reads stdin; `--preview` uses
  `PreviewConfirmer`, which never prompts, so preview never blocks.
- **`--preview`** prints the exact shell command (or the would-be question, or
  the computed failure) **without executing anything or modifying the config**.

## Running tests

```sh
go test ./...            # unit + CLI integration tests
go test -cover ./...     # coverage
```

The CLI tests build the `qmain` binary into a temp dir, point it at a temp
config via `--alt-config`, and drive the full command set end to end (including
`--preview`) in isolated temp directories — no real QEMU VMs are booted.

## Building

```sh
go build -o qmain .
```

