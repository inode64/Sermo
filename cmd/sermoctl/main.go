package main

import (
	"context"
	"os"

	"sermo/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:]))
}
