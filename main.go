package main

import (
	"os"

	"github.com/ReanSn0w/horizon/internal/cli"
)

func main() {
	os.Exit(cli.New(os.Stdin, os.Stdout, os.Stderr).Run(os.Args[1:]))
}
