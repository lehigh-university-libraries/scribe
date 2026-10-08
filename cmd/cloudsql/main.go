// cloudsql runs a finite migration command with an authenticated Private Service Connect
// proxy, then shuts the proxy down. Services use the proxy as a sidecar.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Cloud SQL migration failed")
		os.Exit(1)
	}
}

func commandFor(mode string) ([]string, error) {
	switch mode {
	case "scribe":
		return []string{"/app/scribe-migrate"}, nil
	case "triplet":
		return []string{"/usr/local/bin/triplet", "-config", "/etc/triplet/cloud-run.yaml", "-migrate-presentation-mariadb"}, nil
	default:
		return nil, errors.New("unknown migration command")
	}
}

func run(ctx context.Context) error {
	if len(os.Args) != 2 {
		return errors.New("migration command required")
	}
	args, err := commandFor(os.Args[1])
	if err != nil {
		return err
	}
	instance := os.Getenv("CLOUD_SQL_CONNECTION_NAME")
	if !validInstance(instance) {
		return errors.New("valid cloud SQL connection name required")
	}
	proxy := exec.Command("/cloud-sql-proxy", "--psc", "--structured-logs", "--port=3306", instance) // #nosec G204,G702 -- fixed executable without a shell; the connection name must match a bounded project:region:instance grammar.
	proxy.Stdout, proxy.Stderr = os.Stdout, os.Stderr
	if err := proxy.Start(); err != nil {
		return err
	}
	proxyDone := make(chan struct{})
	go func() { _ = proxy.Wait(); close(proxyDone) }()
	defer func() {
		_ = proxy.Process.Signal(syscall.SIGTERM)
		select {
		case <-proxyDone:
		case <-time.After(5 * time.Second):
			_ = proxy.Process.Kill()
			<-proxyDone
		}
	}()
	readyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := waitForProxy(readyCtx, proxyDone); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204 -- commandFor returns only fixed, reviewed migration executables and arguments.
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func validInstance(name string) bool {
	return regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]:[a-z][a-z0-9-]{1,31}:[a-z][a-z0-9-]{0,97}$`).MatchString(name)
}

func waitForProxy(ctx context.Context, exited <-chan struct{}) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return fmt.Errorf("proxy exited before readiness")
		case <-ticker.C:
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", "127.0.0.1:3306")
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}
