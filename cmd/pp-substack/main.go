package main

import (
	"fmt"
	"os"

	"github.com/theworkflowco/pp-substack/internal/cli"
)

var version = "dev"

func main() {
	command := cli.NewRoot(cli.Options{Version: version})
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
