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
		if output, ok := cli.ErrorOutput(err); ok {
			fmt.Fprintln(os.Stderr, string(output))
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
