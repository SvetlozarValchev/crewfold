package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"crewfold/internal/buildinfo"
	"crewfold/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := cli.New(os.Stdout, os.Stderr, buildinfo.Current())
	os.Exit(app.RunContext(ctx, os.Args[1:]))
}
