// Command sermoctl is the command-line client for the Sermo service monitor.
package main

import (
	"context"
	"os"

	"sermo/internal/cli"
)

func main() {
	//nolint:forbidigo // main cannot return an exit code; os.Exit here is the only way to propagate it.
	os.Exit(cli.Main(context.Background(), os.Args[1:]))
}
