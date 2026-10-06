// Package agent defines how Raun talks to agents: the Runner abstraction,
// the `command` runner, and the versioned report contract agents answer
// with. Nothing here depends on an LLM provider.
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Request is one agent invocation.
type Request struct {
	RunID string
	// Role is "analyst", "lead" or "reconciler".
	Role   string
	Prompt string
	// Dir is the working directory given to the agent.
	Dir     string
	Timeout time.Duration
}

// Output is what an agent process produced.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// Runner executes an agent. Implementations exist per `runner.kind`.
type Runner interface {
	Run(ctx context.Context, req Request) (Output, error)
}

// ErrTimeout reports an agent that did not finish in time.
var ErrTimeout = errors.New("agent timed out")

// CommandRunner runs an external command: the prompt on stdin, the report
// as a JSON object on stdout.
type CommandRunner struct {
	Argv []string
}

// Run starts the command in req.Dir and waits for it. A non-zero exit code
// is reported in Output, not as an error; errors mean the command could not
// run or timed out. The whole process group is killed on timeout.
func (r CommandRunner) Run(ctx context.Context, req Request) (Output, error) {
	if len(r.Argv) == 0 {
		return Output{}, errors.New("command runner: empty argv")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, r.Argv[0], r.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = append(os.Environ(), "RAUN_RUN_ID="+req.RunID, "RAUN_ROLE="+req.Role)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Duration: time.Since(start), ExitCode: cmd.ProcessState.ExitCode()}

	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("%w after %s", ErrTimeout, req.Timeout)
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return out, fmt.Errorf("run %s: %w", r.Argv[0], err)
	}
	return out, nil
}
