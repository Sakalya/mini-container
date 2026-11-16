package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type options struct {
	rootfs, hostname, cgroupParent string
	memory, cpu, pids              string
	command                        []string
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var o options
	flags := flag.NewFlagSet("mini-container run", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&o.rootfs, "rootfs", "", "path to an unpacked, trusted Linux root filesystem")
	flags.StringVar(&o.hostname, "hostname", "mini", "container hostname")
	flags.StringVar(&o.cgroupParent, "cgroup-parent", "/sys/fs/cgroup", "writable cgroup v2 parent")
	flags.StringVar(&o.memory, "memory", "67108864", "memory.max in bytes, or max")
	flags.StringVar(&o.cpu, "cpu", "50000 100000", "cpu.max quota and period in microseconds")
	flags.StringVar(&o.pids, "pids", "32", "pids.max task count, or max")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	o.command = flags.Args()
	if o.rootfs == "" || len(o.command) == 0 || !strings.HasPrefix(o.command[0], "/") {
		return o, errors.New("usage: mini-container run --rootfs DIR [flags] -- /absolute/command [args...]")
	}
	if o.hostname == "" || len(o.hostname) > 64 || strings.ContainsAny(o.hostname, "\x00\r\n") {
		return o, errors.New("hostname must be 1–64 bytes with no NUL or newline")
	}
	for _, limit := range []struct{ name, value string }{
		{"memory", o.memory}, {"pids", o.pids},
	} {
		if limit.value != "max" && !positiveDecimal(limit.value) {
			return o, fmt.Errorf("--%s must be a positive decimal integer or max", limit.name)
		}
	}
	fields := strings.Fields(o.cpu)
	if len(fields) != 2 || (fields[0] != "max" && !positiveDecimal(fields[0])) || !positiveDecimal(fields[1]) {
		return o, errors.New("--cpu must be 'QUOTA PERIOD' or 'max PERIOD'")
	}
	o.cpu = strings.Join(fields, " ")
	return o, nil
}

func positiveDecimal(value string) bool {
	n, err := strconv.ParseUint(value, 10, 63)
	return err == nil && n > 0
}
