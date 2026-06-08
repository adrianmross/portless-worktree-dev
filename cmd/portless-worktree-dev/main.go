package main

import (
	"os"

	"github.com/adrianmross/portless-worktree-dev/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		cli.FprintError(os.Stderr, err)
		os.Exit(1)
	}
}
