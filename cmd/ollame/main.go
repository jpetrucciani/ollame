package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/jpetrucciani/ollame/internal/cli"
)

var version = "dev"
var commit = "unknown"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	notices := make(chan os.Signal, 4)
	reload := make(chan struct{}, 1)
	finished := make(chan struct{})
	signal.Notify(notices, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		terminating := false
		for {
			select {
			case <-finished:
				return
			case notice := <-notices:
				if notice == syscall.SIGHUP {
					if !terminating {
						select {
						case reload <- struct{}{}:
						default:
						}
					}
					continue
				}
				if terminating {
					if sig, ok := notice.(syscall.Signal); ok {
						os.Exit(128 + int(sig))
					}
					os.Exit(1)
				}
				terminating = true
				cancel()
			}
		}
	}()
	app := cli.App{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Env: os.Environ(), Version: version, Commit: commit, Reload: reload}
	status := app.Run(ctx, os.Args[1:])
	signal.Stop(notices)
	close(finished)
	cancel()
	os.Exit(status)
}
