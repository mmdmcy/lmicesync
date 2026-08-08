// lmicesync - peer-to-peer auto-save and repository sync for the LinuxMice
// fleet. Snapshots dirty work into auto/<device> branches and exchanges them
// between devices over the LAN; it never touches public remotes and never
// auto-resolves conflicts.
package main

import (
	"fmt"
	"os"

	"github.com/mmdmcy/lmicesync/internal/config"
	"github.com/mmdmcy/lmicesync/internal/save"
	"github.com/mmdmcy/lmicesync/internal/serve"
	"github.com/mmdmcy/lmicesync/internal/sync"
)

func usage() {
	fmt.Fprintf(os.Stderr, `lmicesync - peer-to-peer auto-save and repo sync

Usage: lmicesync <command> [options]

Commands:
  save                Snapshot dirty repos into auto/<device> branches.
  serve               Run the LAN agent (UDP beacon + HTTP bundle server).
  sync [--host H]     Fetch peer auto refs and merge into auto/merged.
                      --host syncs a specific peer instead of discovered ones.
  peers               List discovered peers.
  init                Write a default config to ~/.config/lmicesync/config.json.
  version             Print the version.

Options:
  --config PATH       Use PATH instead of the default config file.

Config: %s
  device        Stable device name (default: hostname).
  token         Shared secret used to authenticate bundle requests.
  roots         Repo root directories (default ~/Documents/{github,gitlab,forgejo}).
  http_addr     HTTP agent bind address (default :7370).
  beacon_port   UDP discovery port (default 10001).
  require_approval  Require one-time pairing before serving bundles (default true).
  save_interval Minutes between auto-save runs (default 15).
  sync_interval Minutes between sync runs (default 5).
`, config.DefaultPath())
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	path := config.DefaultPath()
	if cmd == "--config" {
		if len(args) < 2 {
			usage()
			os.Exit(2)
		}
		path = args[0]
		cmd = args[1]
		args = args[2:]
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			path = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lmicesync: %v\n", err)
		os.Exit(1)
	}
	switch cmd {
	case "init":
		if err := config.Init(); err != nil {
			fmt.Fprintf(os.Stderr, "lmicesync: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", config.DefaultPath())
	case "version":
		fmt.Println("lmicesync 0.1.0")
	case "save":
		if err := save.Run(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "lmicesync: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := serve.Run(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "lmicesync: %v\n", err)
			os.Exit(1)
		}
	case "sync":
		if err := sync.Run(cfg, args); err != nil {
			fmt.Fprintf(os.Stderr, "lmicesync: %v\n", err)
			os.Exit(1)
		}
	case "peers":
		if err := serve.PrintPeers(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "lmicesync: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}
