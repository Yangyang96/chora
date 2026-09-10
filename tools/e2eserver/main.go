// Command e2eserver runs the legacy Fake-Agent development surface used only
// by the repository's Playwright gates. The production chora serve command
// remains fail-closed behind explicit installation and Preflight identities.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("e2eserver", flag.ContinueOnError)
	databasePath := flags.String("db", "", "test SQLite database path")
	webRoot := flags.String("web", "", "test Web UI root")
	port := flags.Int("port", 0, "test loopback port")
	scenario := flags.String("scenario", "", "explicit test-only scenario")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *databasePath == "" || *webRoot == "" || *port < 1 || *port > 65535 || (*scenario != "" && *scenario != ossAlphaClosureScenario) {
		fmt.Fprintln(os.Stderr, "usage: e2eserver --db PATH --web PATH --port PORT [--scenario oss-alpha-closure]")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	roomServer, err := newRoomServer(ctx, *databasePath, *webRoot, *scenario, log.New(os.Stderr, "chora-e2e: ", log.LstdFlags))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer roomServer.Close()

	httpServer := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)),
		Handler:           roomServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
	}()
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
