package main

import (
	"encoding/json"
	"fmt"
	catalog "github.com/ReanSn0w/horizon/plugins"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "horizon-plugin-metadata" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol_version": 1, "version": "dev", "description": catalog.Description("plugins")})
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Println(catalog.Description("plugins"))
		return
	}
	fmt.Fprintln(os.Stderr, "plugins: release operations are not implemented yet")
	os.Exit(1)
}
