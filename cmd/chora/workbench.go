package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/desktop"
	"github.com/Yangyang96/chora/internal/localweb"
)

func runWorkbench(args []string, stdout, stderr io.Writer) int {
	return runWorkbenchWithListen(args, stdout, stderr, func(server *http.Server) error {
		return server.ListenAndServe()
	})
}

func runWorkbenchWithListen(args []string, stdout, stderr io.Writer, listen func(*http.Server) error) (code int) {
	if listen == nil {
		return 2
	}
	flags := flag.NewFlagSet("workbench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source", "", "absolute Chora source checkout")
	dataRoot := flags.String("data", "", "absolute owner-private Chora data root (default $HOME/.chora/data)")
	webRoot := flags.String("web", "", "built Web UI directory (default SOURCE/web/dist)")
	desktopToken := flags.String("desktop-token", "", "private native-shell authentication token")
	desktopReady := flags.String("desktop-ready", "", "exclusive native-shell readiness file")
	isolatedHelper := flags.String("isolated-helper", "", "packaged Linux arm64 helper")
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
		(*port < 1 && !(*port == 0 && *desktopToken != "")) || *port > 65535 || pathsOverlap(*dataRoot, *sourceRoot) {
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

	if (*desktopToken == "") != (*desktopReady == "") || (*desktopToken != "" && len(*desktopToken) < 32) || (*desktopReady != "" && !canonicalAbsolute(*desktopReady)) {
		fmt.Fprintln(stderr, "desktop mode requires a private token and absolute readiness file")
		return 2
	}
	owner, err := desktop.AcquireOwner(*dataRoot)
	if err != nil {
		fmt.Fprintln(stderr, "workbench data already owned or unavailable:", err)
		return 1
	}
	defer owner.Close()
	var listener net.Listener
	if *desktopToken != "" {
		listener, err = net.Listen("tcp4", "127.0.0.1:"+fmt.Sprint(*port))
		if err != nil {
			fmt.Fprintln(stderr, "workbench loopback listener unavailable:", err)
			return 1
		}
		defer listener.Close()
	}
	ctx := context.Background()
	roomServer, err := localweb.NewWorkbench(ctx, filepath.Join(*dataRoot, "chora.db"), *webRoot, log.New(stderr, "chora: ", log.LstdFlags), localweb.WorkbenchOptions{GracefulShutdown: *desktopToken != "", SourceRoot: *sourceRoot, DataRoot: *dataRoot, PackagedIsolatedHelper: *isolatedHelper})
	if err != nil {
		fmt.Fprintln(stderr, "workbench Chora startup failed")
		return 1
	}
	defer func() {
		if err := roomServer.Close(); err != nil {
			fmt.Fprintln(stderr, "workbench shutdown cleanup requires attention:", err)
			code = 1
		}
	}()

	address := "127.0.0.1:" + fmt.Sprint(*port)
	handler := roomServer.Handler()
	if *desktopToken != "" {
		address = listener.Addr().String()
		application := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/desktop/") {
				w.Header().Set("Cache-Control", "no-store")
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+*desktopToken)) != 1 {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				if r.URL.Path != "/desktop/status" || r.Method != http.MethodGet {
					http.NotFound(w, r)
					return
				}
				activity, err := desktop.Activity(r.Context(), *dataRoot)
				if err != nil {
					http.Error(w, "activity unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(activity)
				return
			}
			application.ServeHTTP(w, r)
		})
	}
	httpServer := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(stdout, "Chora Workbench listening on http://%s\n", address)
	shutdownContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	shutdownDone := make(chan error, 1)
	listeningDone := make(chan struct{})
	defer close(listeningDone)
	go func() {
		select {
		case <-shutdownContext.Done():
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			shutdownDone <- httpServer.Shutdown(ctx)
		case <-listeningDone:
		}
	}()
	if listener != nil {
		ready, err := os.OpenFile(*desktopReady, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(stderr, "desktop readiness file unavailable:", err)
			return 1
		}
		err = json.NewEncoder(ready).Encode(map[string]any{"url": "http://" + address, "pid": os.Getpid()})
		closeErr := ready.Close()
		defer os.Remove(*desktopReady)
		if err != nil || closeErr != nil {
			fmt.Fprintln(stderr, "desktop readiness write failed")
			return 1
		}
		err = httpServer.Serve(listener)
	} else {
		err = listen(httpServer)
	}
	if shutdownContext.Err() != nil {
		if shutdownErr := <-shutdownDone; shutdownErr != nil {
			fmt.Fprintln(stderr, "workbench HTTP shutdown incomplete:", shutdownErr)
			return 1
		}
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "workbench Chora server stopped unexpectedly")
		return 1
	}
	return 0
}
