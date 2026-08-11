package main

import (
	"os"

	"crewfold/internal/buildinfo"
	"crewfold/internal/cli"
)

func main() {
	app := cli.New(os.Stdout, os.Stderr, buildinfo.Current())
	os.Exit(app.Run(os.Args[1:]))
}
