package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"crewfold/internal/buildinfo"
	"crewfold/internal/cli"
	"crewfold/internal/execution"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__direct-supervisor":
			os.Exit(execution.RunDirectSupervisor(os.Args[2:]))
		case "__fixture-provider":
			os.Exit(execution.RunFixtureProvider(os.Stdin, os.Stdout, os.Stderr))
		case "__fixture-mcp-provider":
			os.Exit(execution.RunFixtureMCPProvider(os.Stdin, os.Stdout, os.Stderr))
		case "__herdr-pane-supervisor":
			os.Exit(execution.RunHerdrPaneSupervisor(os.Args[2:]))
		case "__mcp-stdio-bridge":
			os.Exit(execution.RunMCPStdioBridge(os.Stdin, os.Stdout, os.Stderr))
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := cli.New(os.Stdout, os.Stderr, buildinfo.Current())
	os.Exit(app.RunContext(ctx, os.Args[1:]))
}
