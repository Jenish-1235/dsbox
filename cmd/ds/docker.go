package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/Jenish-1235/dsbox/internal/devproj"
	"github.com/Jenish-1235/dsbox/internal/dockui"
	"github.com/Jenish-1235/dsbox/internal/layout"
)

// runDocker is ds docker (ds d): the container UI. --dump prints the main view
// once as plain text (for checks over SSM, where there's no terminal).
// Addresses use DEVBOX_HOST, the VM's name on the developer's tailnet: what the developer's laptop
// uses to reach published ports.
func runDocker(ctx context.Context, l layout.Layout, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("docker", flag.ContinueOnError)
	dump := fs.Bool("dump", false, "print the main view once as plain text")
	width := fs.Int("width", 0, "width for --dump (default $COLUMNS or 100)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host := devproj.LoadSettings(l).Host
	c := dockui.NewClient(dockui.SocketPath())
	if *dump {
		w := *width
		if w <= 0 {
			w, _ = strconv.Atoi(os.Getenv("COLUMNS"))
		}
		if w <= 0 {
			w = 100
		}
		out, err := dockui.Dump(ctx, c, host, w)
		if err != nil {
			return fmt.Errorf("docker: %w", err)
		}
		_, err = io.WriteString(stdout, out)
		return err
	}
	return dockui.Run(ctx, c, host)
}
