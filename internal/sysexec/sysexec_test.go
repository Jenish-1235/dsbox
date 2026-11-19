package sysexec

import (
	"slices"
	"testing"
)

func TestUserBusEnv(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		exists  []string
		want    []string
	}{
		{"login session already has both",
			[]string{"HOME=/h", "XDG_RUNTIME_DIR=/run/user/7", "DBUS_SESSION_BUS_ADDRESS=x"},
			[]string{"/run/user/1002", "/run/user/1002/bus"},
			[]string{"HOME=/h", "XDG_RUNTIME_DIR=/run/user/7", "DBUS_SESSION_BUS_ADDRESS=x"}},
		{"sudo -iu or SSM: added",
			[]string{"HOME=/h"},
			[]string{"/run/user/1002", "/run/user/1002/bus"},
			[]string{"HOME=/h", "XDG_RUNTIME_DIR=/run/user/1002", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1002/bus"}},
		{"no user manager running: left alone",
			[]string{"HOME=/h"}, nil, []string{"HOME=/h"}},
		{"runtime dir without a bus",
			[]string{"HOME=/h"}, []string{"/run/user/1002"},
			[]string{"HOME=/h", "XDG_RUNTIME_DIR=/run/user/1002"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := userBusEnv(tt.environ, "/run/user/1002", func(p string) bool { return slices.Contains(tt.exists, p) })
			if !slices.Equal(got, tt.want) {
				t.Errorf("userBusEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShellEnv(t *testing.T) {
	ctx := t.Context()
	if !ShellEnv(ctx, []string{"DEVBOX_T=1"}, `[ "$DEVBOX_T" = 1 ]`) {
		t.Error("ShellEnv() didn't give the script env")
	}
	if ShellEnv(ctx, []string{"DEVBOX_T=1"}, "exit 3") {
		t.Error("ShellEnv() = true for a failing script")
	}
}
