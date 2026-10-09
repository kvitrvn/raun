package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/workspace"
)

func TestServeOptions(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		code    int
		message string
	}{
		{[]string{"-port", "0"}, 2, "port: must be between"},
		{[]string{"-port", "65536"}, 2, "port: must be between"},
		{[]string{"-port", "-1"}, 2, "port: must be between"},
		{[]string{"-port", "no"}, 2, "invalid value"},
		{[]string{"extra"}, 2, "unexpected argument"},
		{[]string{"-host", "0.0.0.0"}, 2, "flag provided but not defined"},
		{[]string{"-dir", t.TempDir()}, 1, "open workspace"},
		{[]string{"-read-only", "-dir", t.TempDir()}, 1, "open workspace"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, errOut := runCLI(t, append([]string{"serve"}, tt.args...)...)
			if code != tt.code || !strings.Contains(errOut, tt.message) {
				t.Fatalf("code=%d stderr=%s", code, errOut)
			}
		})
	}
	code, _, errOut := runCLI(t, "serve", "-h")
	if code != 0 || (!strings.Contains(errOut, "8080") || !strings.Contains(errOut, "-read-only")) {
		t.Fatalf("help: code=%d stderr=%s", code, errOut)
	}
}

func TestServeOccupiedPort(t *testing.T) {
	root := t.TempDir()
	if _, err := workspace.Init(root); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	code, _, errOut := runCLI(t, "serve", "-dir", root, "-port", strconv.Itoa(port))
	if code != 1 || !strings.Contains(errOut, fmt.Sprintf("listen on 127.0.0.1:%d", port)) {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
}

// Run the real command in a subprocess so an interrupt cannot affect other tests.
func TestServeProcess(t *testing.T) {
	if os.Getenv("RAUN_TEST_SERVE") != "1" {
		return
	}
	os.Exit(run([]string{"serve", "-dir", os.Getenv("RAUN_TEST_PROJECT"), "-port", os.Getenv("RAUN_TEST_PORT")}, os.Stdout, os.Stderr))
}

func TestServeInterrupt(t *testing.T) {
	root := t.TempDir()
	if _, err := workspace.Init(root); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServeProcess$")
	cmd.Env = append(os.Environ(), "RAUN_TEST_SERVE=1", "RAUN_TEST_PROJECT="+root, "RAUN_TEST_PORT="+strconv.Itoa(port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		_ = cmd.Wait()
		t.Fatalf("no URL: %s", stderr.String())
	}
	address := fmt.Sprintf("http://127.0.0.1:%d/knowledge", port)
	if !strings.Contains(scanner.Text(), address) {
		t.Errorf("URL: %q", scanner.Text())
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(body), "No knowledge yet") {
		t.Errorf("page: %s", body)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("shutdown: %v; %s", err, stderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("shutdown timed out: %v", ctx.Err())
	}
	rebound, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listener remained open: %v", err)
	}
	_ = rebound.Close()
}
