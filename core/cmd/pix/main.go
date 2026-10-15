// Command pix runs Claude Code or Codex so DeskPixie can see and reach the session (#89).
//
//	pix [--description "…"] claude|codex [args…]
//	pix hook <event> [payload]   (called by the agent's per-run hooks)
//	pix mcp                      (the agent's per-run MCP server: set_description)
//	pix version
package main

import (
	"fmt"
	"os"

	"github.com/sihekuang/deskpixie/core/pix"
)

var version = "dev" // -ldflags "-X main.version=…"

func main() {
	home, _ := os.UserHomeDir()
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "hook":
			if len(args) < 2 {
				os.Exit(0)
			}
			payload := ""
			if len(args) > 2 {
				payload = args[len(args)-1]
			}
			os.Exit(pix.RunHook(args[1], os.Getenv("PIX_SOCK"), payload, os.Stdin, os.Stdout))
		case "mcp":
			if err := pix.RunMCP(os.Getenv("PIX_SOCK"), os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "version", "--version":
			fmt.Println("pix", version)
			os.Exit(0)
		}
	}
	os.Exit(pix.Run(args, os.Getenv, home))
}
