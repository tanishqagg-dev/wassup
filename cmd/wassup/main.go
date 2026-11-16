// Command wassup is a WhatsApp client for the terminal and for AI agents.
package main

import (
	"os"

	"github.com/tanishqagg-dev/wassup/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
