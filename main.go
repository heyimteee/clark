package main

import (
	"errors"
	"os"

	"github.com/tristnaja/clark/cmd"
	"github.com/tristnaja/clark/internal/whatsapp"
)

func main() {
	var err error
	commands := map[string]struct{}{
		"init":   {},
		"run":    {},
		"vip":    {},
		"ctx":    {},
		"toggle": {},
		"view":   {},
	}

	if len(os.Args) < 2 {
		whatsapp.Fatalf("USAGE", "usage: clark [cmd]")
	}

	if _, exist := commands[os.Args[1]]; !exist {
		whatsapp.Fatalf("USAGE", "unknown command '%v'", os.Args[1])
	}

	if len(os.Args) > 2 && os.Args[1] == "run" {
		whatsapp.Fatalf("USAGE", "unnecessary argument(s), usage: clark run")
	}

	if len(os.Args) < 3 && (os.Args[1] == "vip" || os.Args[1] == "ctx") {
		whatsapp.Fatalf("USAGE", "usage: clark %v [args]", os.Args[1])
	}

	ast, err := whatsapp.AssistantInit()

	if err != nil {
		whatsapp.Fatalf("ASSIST", "fail to create assistant: %v", err)
	}

	switch os.Args[1] {
	case "init":
		err = cmd.ExecInit(ast)
	case "run":
		err = cmd.ExecRun(ast)
	case "vip":
		err = cmd.ExecVIP(os.Args[2:], ast)
	case "ctx":
		err = cmd.ExecContext(os.Args[2:], ast)
	case "toggle":
		err = cmd.ExecToggle(ast)
	case "view":
		cmd.ExecView(ast)
	default:
		err = errors.New("unknown command sir, here are the commands: init, run, add, ctx, toggle")
	}

	if err != nil {
		whatsapp.Fatalf("CMD", "%v", err)
	}

	ast.DB.DB.Close()
}
