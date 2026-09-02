package main

import (
	"context"
	"os"

	"crewfold/internal/buildinfo"
	"crewfold/internal/roomcli"
)

func main() {
	ctx, stop := notifyContext(context.Background())
	defer stop()

	app := roomcli.New(os.Stdout, os.Stderr, buildinfo.Current())
	os.Exit(app.Run(ctx, os.Args[1:]))
}
