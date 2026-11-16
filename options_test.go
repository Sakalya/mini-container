package main

import (
	"io"
	"testing"
)

func TestParseOptions(t *testing.T) {
	o, err := parseOptions([]string{"--rootfs", "/rootfs", "--cpu", "max  100000", "--", "/bin/sh", "-c", "echo ok"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.cpu != "max 100000" || o.memory != "67108864" || o.pids != "32" ||
		len(o.command) != 3 || o.command[1] != "-c" {
		t.Fatalf("unexpected options: %+v", o)
	}
}

func TestRejectInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--", "/bin/sh"},
		{"--rootfs", "/rootfs", "--", "sh"},
		{"--rootfs", "/rootfs", "--pids", "0", "--", "/bin/sh"},
		{"--rootfs", "/rootfs", "--memory", "-1", "--", "/bin/sh"},
		{"--rootfs", "/rootfs", "--cpu", "max", "--", "/bin/sh"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}
