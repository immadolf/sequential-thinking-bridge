package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sequential-thinking-bridge/internal/server"
	"sequential-thinking-bridge/internal/session"
)

func main() {
	os.Exit(dispatch(os.Args))
}

func dispatch(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sequential-thinking-bridge <serve|healthcheck>")
		return 2
	}

	switch args[1] {
	case "serve":
		if err := serve(args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			return 1
		}
		return 0
	case "healthcheck":
		if err := healthcheck(args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[1])
		return 2
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:38989", "local listen address")
	path := fs.String("path", "/mcp", "MCP HTTP path")
	token := fs.String("token", "", "optional Bearer token")
	ttl := fs.Duration("session-ttl", 2*time.Hour, "idle thought handle TTL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateLocalAddress(*listen); err != nil {
		return err
	}

	store := session.NewStore(*ttl)
	handler := server.New(server.Config{Store: store, Token: *token})

	mux := http.NewServeMux()
	mcpPath := *path
	if !strings.HasPrefix(mcpPath, "/") {
		mcpPath = "/" + mcpPath
	}
	mux.Handle(mcpPath, handler)
	mux.Handle("/healthz", handler)

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go cleanupLoop(store, *ttl)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "sequential-thinking-bridge listening on http://%s%s\n", *listen, mcpPath)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:38989", "local listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateLocalAddress(*listen); err != nil {
		return err
	}
	fmt.Println("config OK")
	return nil
}

func cleanupLoop(store *session.Store, ttl time.Duration) {
	interval := ttl / 2
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for now := range ticker.C {
		_ = store.Cleanup(now)
	}
}

func validateLocalAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("server must bind to 127.0.0.1, localhost, or ::1; got %s", host)
}
