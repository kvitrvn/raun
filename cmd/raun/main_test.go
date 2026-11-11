package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "no command prints usage on stderr",
			args:       nil,
			wantCode:   exitUsage,
			wantStderr: []string{"Usage:", "server", "runner", "version"},
		},
		{
			name:       "help prints usage on stdout",
			args:       []string{"help"},
			wantCode:   exitOK,
			wantStdout: []string{"Usage:", "server", "runner", "version"},
		},
		{
			name:       "short help flag",
			args:       []string{"-h"},
			wantCode:   exitOK,
			wantStdout: []string{"Usage:"},
		},
		{
			name:       "long help flag",
			args:       []string{"--help"},
			wantCode:   exitOK,
			wantStdout: []string{"Usage:"},
		},
		{
			name:       "version",
			args:       []string{"version"},
			wantCode:   exitOK,
			wantStdout: []string{"raun "},
		},
		{
			name:       "server is not implemented",
			args:       []string{"server"},
			wantCode:   exitNotImplemented,
			wantStderr: []string{"raun server: not implemented yet"},
		},
		{
			name:       "runner is not implemented",
			args:       []string{"runner"},
			wantCode:   exitNotImplemented,
			wantStderr: []string{"raun runner: not implemented yet"},
		},
		{
			name:       "unknown command",
			args:       []string{"deploy"},
			wantCode:   exitUsage,
			wantStderr: []string{`unknown command "deploy"`, "Usage:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			assertStream(t, "stdout", stdout.String(), tt.wantStdout)
			assertStream(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

// assertStream checks that got contains every wanted substring, and that it is
// empty when nothing is wanted.
func assertStream(t *testing.T, name, got string, want []string) {
	t.Helper()
	if len(want) == 0 {
		if got != "" {
			t.Errorf("%s = %q, want empty", name, got)
		}
		return
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, want it to contain %q", name, got, w)
		}
	}
}

func TestVersionLine(t *testing.T) {
	tests := []struct {
		name string
		info buildInfo
		want string
	}{
		{name: "no build information", info: buildInfo{}, want: "raun dev"},
		{name: "development build", info: buildInfo{version: "(devel)"}, want: "raun dev"},
		{name: "tagged build", info: buildInfo{version: "v1.2.3"}, want: "raun v1.2.3"},
		{
			name: "pseudo-version already names the revision",
			info: buildInfo{version: "v0.0.0-20261111203742-a8bd60891589+dirty", revision: "a8bd60891589f119", modified: true},
			want: "raun v0.0.0-20261111203742-a8bd60891589+dirty",
		},
		{
			name: "revision and time",
			info: buildInfo{version: "(devel)", revision: "0123456789abcdef", time: "2026-11-11T20:00:00Z"},
			want: "raun dev (0123456789ab, 2026-11-11T20:00:00Z)",
		},
		{
			name: "modified working tree",
			info: buildInfo{revision: "0123456789abcdef", modified: true},
			want: "raun dev (0123456789ab-dirty)",
		},
		{
			name: "short revision is kept whole",
			info: buildInfo{revision: "abc123"},
			want: "raun dev (abc123)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionLine(tt.info); got != tt.want {
				t.Errorf("versionLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
