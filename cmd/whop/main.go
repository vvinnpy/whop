package main

import (
	"os"

	"whop/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.NewDeps()))
}
