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
	// MaxOutputBytes bounds stdout; 0 means no limit.
	MaxOutputBytes int64
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

// ErrTimeout reports an agent that did not finish in time. Output still
// holds what it printed before it was stopped.
var ErrTimeout = errors.New("agent timed out")

// ErrOutputTooLarge reports an agent stopped for printing more than
// Request.MaxOutputBytes on stdout.
var ErrOutputTooLarge = errors.New("agent output too large")

// ErrInterrupted reports an agent stopped because its caller's context was
// canceled, as when the run is interrupted.
var ErrInterrupted = errors.New("interrupted")

// CommandRunner runs an external command: the prompt on stdin, the report
// as a JSON object on stdout.
type CommandRunner struct {
	Argv []string
}

// Run starts the command in req.Dir and waits for it. A non-zero exit code
// is reported in Output, not as an error; errors mean the command could not
// run, timed out, printed too much or was interrupted by ctx. The whole
// process group is killed in the last three cases, and Output keeps what
// was printed until then.
func (r CommandRunner) Run(ctx context.Context, req Request) (Output, error) {
	if len(r.Argv) == 0 {
		return Output{}, errors.New("command runner: empty argv")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	cmd := exec.CommandContext(ctx, r.Argv[0], r.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = append(os.Environ(), "RAUN_RUN_ID="+req.RunID, "RAUN_ROLE="+req.Role)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, max: req.MaxOutputBytes, stop: stop}
	cmd.Stderr = &stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Duration: time.Since(start), ExitCode: cmd.ProcessState.ExitCode()}

	if errors.Is(context.Cause(ctx), ErrOutputTooLarge) {
		return out, fmt.Errorf("%w: over %d bytes", ErrOutputTooLarge, req.MaxOutputBytes)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("%w after %s", ErrTimeout, req.Timeout)
	}
	if ctx.Err() == context.Canceled {
		return out, ErrInterrupted
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return out, fmt.Errorf("run %s: %w", r.Argv[0], err)
	}
	return out, nil
}

// limitedWriter keeps at most max bytes (0: no limit) and stops the agent
// with ErrOutputTooLarge when it prints more.
type limitedWriter struct {
	buf  *bytes.Buffer
	max  int64
	stop context.CancelCauseFunc
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.max > 0 && int64(w.buf.Len()+len(p)) > w.max {
		w.buf.Write(p[:max(0, w.max-int64(w.buf.Len()))])
		w.stop(ErrOutputTooLarge)
		return 0, ErrOutputTooLarge
	}
	return w.buf.Write(p)
}
