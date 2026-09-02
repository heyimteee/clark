package main

import (
	"os"

	"github.com/heyimteee/clark/internal/app"
	"github.com/heyimteee/clark/internal/install"
	"github.com/heyimteee/clark/internal/logging"
)

// version is stamped at build time (-X main.version=...) by goreleaser and
// the Docker build; unstamped builds report "dev".
var version = "dev"

func main() {
	commands := map[string]struct{}{
		"init":    {},
		"run":     {},
		"vip":     {},
		"ctx":     {},
		"toggle":  {},
		"think":   {},
		"history": {},
		"view":    {},
		"access":  {},
		"help":    {},
		"install": {},
		"config":  {},
	}

	if len(os.Args) < 2 {
		logging.Fatalf("USAGE", "usage: clark [cmd]")
	}

	if _, exist := commands[os.Args[1]]; !exist {
		logging.Fatalf("USAGE", "unknown command '%v'", os.Args[1])
	}

	if len(os.Args) > 2 && os.Args[1] == "run" {
		logging.Fatalf("USAGE", "unnecessary argument(s), usage: clark run")
	}

	if os.Args[1] == "install" {
		if err := install.Run(os.Args[2:]); err != nil {
			logging.Fatalf("INSTALL", "%v", err)
		}
		return
	}
	if os.Args[1] == "config" {
		if err := install.Config(os.Args[2:]); err != nil {
			logging.Fatalf("CONFIG", "%v", err)
		}
		return
	}

	if len(os.Args) < 3 && (os.Args[1] == "vip" || os.Args[1] == "ctx" || os.Args[1] == "access" || os.Args[1] == "think" || os.Args[1] == "history") {
		logging.Fatalf("USAGE", "usage: clark %v [args]", os.Args[1])
	}

	a, err := app.New(version)
	if err != nil {
		logging.Fatalf("ASSIST", "fail to create assistant: %v", err)
	}
	defer a.Close()

	switch os.Args[1] {
	case "init":
		err = a.Init()
	case "run":
		err = a.Run()
	case "vip":
		err = a.VIP(os.Args[2:])
	case "ctx":
		err = a.Context(os.Args[2:])
	case "toggle":
		err = a.Toggle(os.Args[2:])
	case "think":
		err = a.Think(os.Args[2:])
	case "history":
		err = a.History(os.Args[2:])
	case "view":
		err = a.View()
	case "access":
		err = a.Access(os.Args[2:])
	case "help":
		err = a.Help()
	}

	if err != nil {
		logging.Fatalf("CMD", "%v", err)
	}
}
