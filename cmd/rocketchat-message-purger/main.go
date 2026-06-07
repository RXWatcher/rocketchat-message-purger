package main

import (
	"context"
	"os"

	"rocketchat-message-purger/internal/cli"
)

func main() {
	exitCode := cli.Main(context.Background(), os.Args[1:], cli.EnvFromOS(), os.Stdout, os.Stderr, nil)
	os.Exit(exitCode)
}
