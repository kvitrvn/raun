package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kvitrvn/raun/internal/web"
)

func cmdServe(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	dir := fs.String("dir", ".", "repository root")
	port := fs.Int("port", 8080, "local HTTP port (1-65535)")
	readOnly := fs.Bool("read-only", false, "disable human decisions; consultation only")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("%w: port: must be between 1 and 65535", errUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return web.Serve(ctx, *dir, *port, stdout, web.Options{ReadOnly: *readOnly})
}
