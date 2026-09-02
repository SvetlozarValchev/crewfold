package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"crewfold/internal/buildinfo"
	"crewfold/internal/roomcli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := roomcli.New(os.Stdout, os.Stderr, buildinfo.Current())
	os.Exit(app.Run(ctx, os.Args[1:]))
}
