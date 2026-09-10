package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Yangyang96/chora/internal/localweb"
)

func runWorkbench(args []string, stdout, stderr io.Writer) int {
	return runWorkbenchWithListen(args, stdout, stderr, func(server *http.Server) error {
		return server.ListenAndServe()
	})
}

func runWorkbenchWithListen(args []string, stdout, stderr io.Writer, listen func(*http.Server) error) int {
	if listen == nil {
		return 2
	}
	flags := flag.NewFlagSet("workbench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source", "", "absolute Chora source checkout")
	dataRoot := flags.String("data", "", "absolute owner-private Chora data root (default $HOME/.chora/data)")
	webRoot := flags.String("web", "", "built Web UI directory (default SOURCE/web/dist)")
	port := flags.Int("port", 8787, "loopback Chora port")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	defaultDataRoot := *dataRoot == ""
	if defaultDataRoot {
		home, err := os.UserHomeDir()
		if err != nil || !canonicalAbsolute(home) {
			fmt.Fprintln(stderr, "workbench cannot resolve home directory; specify absolute --data")
			return 2
		}
		*dataRoot = filepath.Join(home, ".chora", "data")
	}
	if flags.NArg() != 0 || !canonicalAbsolute(*sourceRoot) || !canonicalAbsolute(*dataRoot) ||
		*port < 1 || *port > 65535 || pathsOverlap(*dataRoot, *sourceRoot) {
		fmt.Fprintln(stderr, "workbench requires absolute --source, --data, a valid --port, and a data root outside the source checkout")
		return 2
	}
	if *webRoot == "" {
		*webRoot = filepath.Join(*sourceRoot, "web", "dist")
	}
	if !canonicalAbsolute(*webRoot) || !pathInside(*sourceRoot, *webRoot) {
		fmt.Fprintln(stderr, "workbench Web build must be inside the source checkout")
		return 2
	}
	if defaultDataRoot {
		if err := ensureSourceCheckoutDataRoot(filepath.Dir(*dataRoot)); err != nil {
			fmt.Fprintln(stderr, "workbench default data parent is unavailable")
			return 1
		}
	}
	if err := ensureSourceCheckoutDataRoot(*dataRoot); err != nil {
		fmt.Fprintln(stderr, "workbench data root is unavailable")
		return 1
	}

	ctx := context.Background()
	roomServer, err := localweb.NewWorkbench(ctx, filepath.Join(*dataRoot, "chora.db"), *webRoot, log.New(stderr, "chora: ", log.LstdFlags), localweb.WorkbenchOptions{SourceRoot: *sourceRoot, DataRoot: *dataRoot})
	if err != nil {
		fmt.Fprintln(stderr, "workbench Chora startup failed")
		return 1
	}
	defer roomServer.Close()

	address := "127.0.0.1:" + fmt.Sprint(*port)
	httpServer := &http.Server{Addr: address, Handler: roomServer.Handler(), ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(stdout, "Chora Workbench listening on http://%s\n", address)
	err = listen(httpServer)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "workbench Chora server stopped unexpectedly")
		return 1
	}
	return 0
}
