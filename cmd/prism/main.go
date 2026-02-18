package main

import (
	"os"

	"github.com/dshills/prism/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}
