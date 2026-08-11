package main

import (
	"os"

	"github.com/heyimteee/clark/internal/app"
	"github.com/heyimteee/clark/internal/logging"
)

func main() {
	commands := map[string]struct{}{
		"init":   {},
		"run":    {},
		"vip":    {},
		"ctx":    {},
		"toggle": {},
		"view":   {},
		"access": {},
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

	if len(os.Args) < 3 && (os.Args[1] == "vip" || os.Args[1] == "ctx" || os.Args[1] == "access") {
		logging.Fatalf("USAGE", "usage: clark %v [args]", os.Args[1])
	}

	a, err := app.New()
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
		err = a.Toggle()
	case "view":
		err = a.View()
	case "access":
		err = a.Access(os.Args[2:])
	}

	if err != nil {
		logging.Fatalf("CMD", "%v", err)
	}
}
