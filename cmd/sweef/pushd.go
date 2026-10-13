package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sweefbrowser/sweef/internal/pushd"
)

// cmdPushd runs the self-hostable Web Push relay (docs/push.md).
func cmdPushd(args []string) {
	fs := flag.NewFlagSet("pushd", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "listen address (put TLS in front of it)")
	public := fs.String("public-url", "", "external base URL used in endpoints, e.g. https://push.example.com")
	data := fs.String("data", "", "directory for the secret and undelivered messages (empty: memory only)")
	maxTTL := fs.Duration("max-ttl", 30*24*time.Hour, "longest time a message is kept")
	queue := fs.Int("queue", 100, "stored messages per browser")
	rate := fs.Float64("rate", 50, "requests per second per source address")
	fwd := fs.Bool("trust-forwarded", false, "take the client address from X-Forwarded-For (only behind your own proxy)")
	vapid := fs.Bool("require-vapid", false, "refuse posts without a valid VAPID header")
	_ = fs.Parse(args)
	s, err := pushd.New(pushd.Config{PublicURL: *public, DataDir: *data, MaxTTL: *maxTTL, MaxQueuePerClient: *queue,
		IPRate: *rate, IPBurst: *rate * 2, TrustForwarded: *fwd, RequireVAPID: *vapid,
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sweef pushd:", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	fmt.Fprintf(os.Stderr, "sweef pushd listening on %s\n", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "sweef pushd:", err)
		s.Close()
		os.Exit(1)
	}
	_ = s.Close()
}
