package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "horizon-plugin-metadata" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol_version": 1, "version": "1.0.0", "description": "Local example command"})
		return
	}
	if len(os.Args) == 1 || os.Args[1] == "--help" || os.Args[1] == "-h" {
		fmt.Fprintln(os.Stdout, "Usage: horizon example greet [name]")
		return
	}
	if os.Args[1] == "greet" && len(os.Args) <= 3 {
		name := "world"
		if len(os.Args) == 3 {
			name = os.Args[2]
		}
		fmt.Fprintf(os.Stdout, "Hello, %s!\n", name)
		return
	}
	fmt.Fprintln(os.Stderr, "example: unknown command")
	os.Exit(2)
}
