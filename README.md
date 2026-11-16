# mini-container

A small Linux container runtime written in Go with no external Go dependencies. It runs one command from an unpacked root filesystem with PID, UTS, and mount namespaces and cgroup v2 limits.

## Requirements

- A disposable Linux VM, Linux 5.7 or newer, Go 1.22 or newer, and root privileges. The kernel must support the requested namespaces, cgroup v2, and `clone3(CLONE_INTO_CGROUP)`. A seccomp profile can block the latter even on a recent kernel.
- A writable cgroup v2 parent with the `memory`, `cpu`, and `pids` controllers enabled for its children in `cgroup.subtree_control`. The parent must permit process placement in a new child cgroup. The default parent is `/sys/fs/cgroup`. On a managed host, use an appropriately delegated subtree with `--cgroup-parent`.
- A trusted, architecture-matching Linux root filesystem containing your executable, any required interpreter or shared libraries, and a real `/proc` directory. The host also needs a mounted procfs for `/proc/self/exe`.

## Build and run

From inside the Linux VM, build the runtime and run the unit tests.

```sh
go build -o mini-container .
go test ./...
go vet ./...
```

For example, use Docker in the VM to export a trusted BusyBox root filesystem into a fresh directory. Docker is only used here to prepare the files.

```sh
mkdir rootfs
id=$(docker create busybox:1.37)
docker export "$id" | tar -xf - -C rootfs
docker rm "$id"
mkdir -p rootfs/proc

sudo ./mini-container run --rootfs ./rootfs --hostname demo -- /bin/sh -c '
  echo "PID=$$"
  hostname
  cat /proc/1/comm
  ps
'
```

Expect PID 1 and hostname `demo`. The process list shows processes from the new PID namespace. Compare `hostname` outside the run to verify the VM hostname was preserved.

Supply flags before `--` and an absolute executable path inside the rootfs after it.

```sh
sudo ./mini-container run --rootfs ./rootfs \
  --memory 134217728 --cpu '25000 100000' --pids 16 \
  -- /bin/sh -c 'echo hello'
```

| Flag | Kernel control | Default | Meaning |
| --- | --- | --- | --- |
| `--memory` | `memory.max` | `67108864` | Memory ceiling in bytes, or `max`. The kernel may reclaim memory and invoke the cgroup OOM killer. |
| `--cpu` | `cpu.max` | `50000 100000` | Aggregate CPU time per period, in microseconds. The default is half of one CPU's time. Use `max PERIOD` for no quota at this level. |
| `--pids` | `pids.max` | `32` | Maximum number of tasks, including threads, or `max`. Leave room for the Go bootstrap threads as well as the workload. |

Ancestor limits still apply. Memory swap has a separate kernel control.

## How it works

1. **Namespaces.** The parent starts a copy of itself with `CLONE_NEWPID`, `CLONE_NEWUTS`, and `CLONE_NEWNS`. The child sees itself as PID 1 and sets its own hostname. The parent keeps the original namespaces.
2. **Root filesystem.** The child makes mount propagation recursively private and bind-mounts the rootfs onto itself. It then changes into that mount, calls `pivot_root(".", ".")`, detaches the former root, and mounts a fresh procfs at `/proc`. The new procfs exposes processes in the child's PID namespace. The child replaces itself with the requested program.
3. **Resource limits.** Before starting the child, the parent creates a unique leaf cgroup and writes all three limits. Go's Linux process launcher uses `clone3` with `CLONE_INTO_CGROUP` so the child starts inside the configured group. The parent waits and then removes its leaf cgroup, including on setup errors. Namespace-local mounts disappear when their last references are released.

The payload is PID 1 and is responsible for reaping orphaned children if its workload creates them. The launcher responds to INT, TERM, and HUP by killing PID 1 and waiting for the namespace to shut down. This is immediate termination. The launcher reports ordinary payload exit codes, uses `128 + signal` for signal exits, uses `125` for runtime errors, and uses `2` for invalid command-line usage. An uncatchable kill of the launcher can leave an empty cgroup directory behind. After verifying it is empty, remove that individual directory with `rmdir`.

## Verification and troubleshooting

Verified with Go 1.22 in Docker Desktop's ARM64 Linux VM using a privileged test container. Linux build, unit tests, and vet passed. Smoke checks covered PID 1, hostname isolation, rootfs visibility, namespace-local procfs, all three cgroup settings, cgroup removal, payload exit-code propagation, and a missing executable. The smoke checks inspect the configured limits without benchmarking CPU throttling or triggering the memory ceiling. Linux AMD64 was cross-compiled.

The unit tests cover argument parsing, defaults, and rejection of malformed limits. For a manual cgroup check, run `/bin/sleep 30` as the payload and inspect the new `mini-container-*` directory from another terminal. Its `cgroup.procs` should include the payload's host PID and the three limit files should match the flags. The directory should disappear when the run ends.

- **Missing limit file.** Check the parent's `cgroup.controllers` and `cgroup.subtree_control`. A controller must be available and enabled at the parent before a child exposes its limit file.
- **Read-only filesystem or permission denied.** Check that the cgroup subtree is writable and that the launcher has permission to create namespaces and mount filesystems.
- **Child start fails with function not implemented or operation not permitted.** Check Linux kernel support, seccomp restrictions, and cgroup delegation. A regular Docker container typically restricts this setup.
- **Executable exists but exec reports no such file.** Check the architecture and whether the rootfs contains the program's ELF interpreter or the script's shebang interpreter.
- **Pivot or proc mount fails.** Check the rootfs path and verify that `proc` is a directory inside it. Use a freshly unpacked rootfs with the required files rather than relying on host submounts.

## Security and scope

Run only trusted payloads and root filesystems in a disposable Linux VM. The workload retains host-root identity and privileges permitted to the launcher. It shares the host's network and IPC namespaces and inherits the launcher's standard streams. The rootfs bind mount is writable, so workload writes persist in the supplied directory. There is no user namespace, capability dropping, seccomp policy, read-only rootfs, device policy, network configuration, image manager, or OCI compatibility. Treat the VM as the security boundary.

## References

- [Linux namespaces overview](https://man7.org/linux/man-pages/man7/namespaces.7.html)
- [PID namespace semantics](https://man7.org/linux/man-pages/man7/pid_namespaces.7.html)
- [pivot_root and the stacked-root technique](https://man7.org/linux/man-pages/man2/pivot_root.2.html)
- [clone3 and CLONE_INTO_CGROUP](https://man7.org/linux/man-pages/man2/clone.2.html)
- [Linux kernel cgroup v2 documentation](https://docs.kernel.org/admin-guide/cgroup-v2.html)
