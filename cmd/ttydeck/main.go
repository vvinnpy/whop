package main

import (
	"os"

	"ttydeck/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.NewDeps()))
}
