package main

import (
	"fmt"
	"os"

	"github.com/sivchari/rask/internal/cluster"
)

func main() {
	homeDir, err := cluster.DefaultHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := newRootCommand(newPlatformRuntime(), homeDir).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
