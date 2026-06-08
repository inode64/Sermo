// Command sermoctl is the command-line client for the Sermo service monitor.
package main

import (
	"context"
	"os"

	"sermo/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:]))
}
