package cmd

import (
	"fmt"

	"github.com/tristnaja/clark/internal/whatsapp"
)

func ExecView(ast *whatsapp.Assistant) error {
	available, err := ast.CheckAst()

	if err != nil {
		return err
	}

	if !available {
		return fmt.Errorf("No assistant is initiated Sir. Do 'clark init' first.")
	}

	whatsapp.Log("CLARK", whatsapp.SevInfo, "VIEW", "Settings",
		"name", ast.Name,
		"model", ast.Model,
		"status", ast.Status,
		"context", ast.MasterContext)
	whatsapp.Log("MEMORY", whatsapp.SevInfo, "VIPLIST", "Current VIP list")
	for jid, name := range ast.VIP.VIP {
		whatsapp.Log("MEMORY", whatsapp.SevInfo, "VIPLIST", "VIP entry", "jid", jid, "relation", name)
	}

	return nil
}
