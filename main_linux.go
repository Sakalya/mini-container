package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

const cgroup2Magic = 0x63677270

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__init" {
		if err := enterContainer(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "container setup:", err)
			os.Exit(125)
		}
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: mini-container run --rootfs DIR [flags] -- /absolute/command [args...]")
		os.Exit(2)
	}
	o, err := parseOptions(os.Args[2:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	code, err := runContainer(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func runContainer(o options) (code int, err error) {
	if os.Geteuid() != 0 {
		return 125, errors.New("run as root on a Linux test VM")
	}
	rootfs, err := filepath.Abs(o.rootfs)
	if err != nil {
		return 125, err
	}
	rootfs, err = filepath.EvalSymlinks(rootfs)
	if err != nil {
		return 125, fmt.Errorf("resolve rootfs: %w", err)
	}
	if rootfs == "/" {
		return 125, errors.New("rootfs cannot be the host root")
	}
	if err := requireDirectory(filepath.Join(rootfs, "proc")); err != nil {
		return 125, err
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs(o.cgroupParent, &fs); err != nil {
		return 125, fmt.Errorf("inspect cgroup parent: %w", err)
	}
	if fs.Type != cgroup2Magic {
		return 125, errors.New("--cgroup-parent must be on a cgroup v2 filesystem")
	}
	dir, err := os.MkdirTemp(o.cgroupParent, "mini-container-")
	if err != nil {
		return 125, fmt.Errorf("create cgroup: %w", err)
	}
	defer func() {
		if cleanupErr := os.Remove(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove cgroup %s: %w", dir, cleanupErr))
			if code == 0 {
				code = 125
			}
		}
	}()

	for _, limit := range []struct{ file, value string }{
		{"memory.max", o.memory}, {"cpu.max", o.cpu}, {"pids.max", o.pids},
	} {
		path := filepath.Join(dir, limit.file)
		file, openErr := os.OpenFile(path, os.O_WRONLY, 0)
		if openErr != nil {
			return 125, fmt.Errorf("open %s (check parent controllers): %w", path, openErr)
		}
		_, writeErr := file.WriteString(limit.value + "\n")
		if limitErr := errors.Join(writeErr, file.Close()); limitErr != nil {
			return 125, fmt.Errorf("set %s: %w", path, limitErr)
		}
	}

	cgroup, err := os.Open(dir)
	if err != nil {
		return 125, fmt.Errorf("open cgroup directory: %w", err)
	}
	defer cgroup.Close()

	args := append([]string{"__init", rootfs, o.hostname}, o.command...)
	cmd := exec.Command("/proc/self/exe", args...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "TERM=" + os.Getenv("TERM")}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNS,
		UseCgroupFD: true,
		CgroupFD:    int(cgroup.Fd()),
		Pdeathsig:   syscall.SIGKILL,
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return 125, fmt.Errorf("start namespaced child in cgroup (requires clone3 and CLONE_INTO_CGROUP): %w", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err = <-wait:
	case sig := <-signals:
		// PID 1 can ignore ordinary signals. Killing it tears down its PID namespace.
		killErr := cmd.Process.Kill()
		err = <-wait
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return 125, fmt.Errorf("terminate container: %w", killErr)
		}
		return 128 + int(sig.(syscall.Signal)), nil
	}

	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status := exit.Sys().(syscall.WaitStatus)
		if status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 125, fmt.Errorf("wait for container: %w", err)
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", path)
	}
	return nil
}

func enterContainer(args []string) error {
	if len(args) < 3 || os.Getpid() != 1 {
		return errors.New("internal init requires PID 1 and a rootfs, hostname and command")
	}
	rootfs, hostname, command := args[0], args[1], args[2:]
	if err := syscall.Sethostname([]byte(hostname)); err != nil {
		return fmt.Errorf("set hostname: %w", err)
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mount tree private: %w", err)
	}
	if err := syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind mount rootfs: %w", err)
	}
	if err := syscall.Chdir(rootfs); err != nil {
		return fmt.Errorf("enter rootfs: %w", err)
	}
	// Stacking both roots at "." avoids creating a directory in the supplied rootfs.
	if err := syscall.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot root: %w", err)
	}
	if err := syscall.Unmount(".", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("enter new root: %w", err)
	}
	if err := requireDirectory("/proc"); err != nil {
		return err
	}
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount proc: %w", err)
	}
	return fmt.Errorf("exec %s: %w", command[0], syscall.Exec(command[0], command, os.Environ()))
}
