package whatsapp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type VIP struct {
	DB    *Database
	Regex *regexp.Regexp
	VIP   map[string]string
}

func InitVIP(db *Database) *VIP {
	return &VIP{
		DB:    db,
		Regex: regexp.MustCompile(`^[0-9]{1,15}\s*,\s*[\p{L}\s]{1,50}\s*,\s*[\p{L}\s]{1,50}$`),
		VIP:   make(map[string]string),
	}
}

func (vip *VIP) LoadVIP() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var jid, name, relation string
	query := `SELECT jid, name, relation FROM vip`

	rows, err := vip.DB.DB.QueryContext(ctx, query)

	if err != nil {
		return fmt.Errorf("fail to load table <vip>: %w", err)
	}

	for rows.Next() {
		err := rows.Scan(&jid, &name, &relation)
		if err != nil {
			return fmt.Errorf("fail to scan jid and relation: %w", err)
		}

		relation = fmt.Sprintf("%v (%v)", name, relation)

		vip.VIP[jid] = relation
	}

	if len(vip.VIP) < 1 {
		Log("MEMORY", SevNotice, "VIPLOAD", "VIP list is empty")
	}

	return nil
}

func (vip *VIP) AddVIP(input string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if len(input) > 100 {
		return fmt.Errorf("my apologies, Sir, but that entry is far too long to process")
	}

	if input == "" {
		return fmt.Errorf("Input is empty sir! Format: [number], [name], [relation]")
	}

	cleaned := strings.TrimPrefix(strings.TrimSpace(input), "+")

	if !vip.Regex.MatchString(cleaned) {
		return fmt.Errorf("forgive me, Sir, the format is invalid. " +
			"Please use: [Number], [Relation]. The name should be letters only")
	}

	parts := strings.Split(input, ",")

	if len(parts) != 3 {
		return fmt.Errorf("my apologies Sir, I require exactly three details: Number, Name, Relation")
	}

	jid, err := sanitizeJID(strings.TrimSpace(parts[0]))

	if err != nil {
		return err
	}

	name := strings.TrimSpace(parts[1])
	rel := strings.TrimSpace(parts[2])

	if strings.TrimSpace(parts[0]) == "" || name == "" || rel == "" {
		return fmt.Errorf("my apologies Sir. Number, name, and relation are required")
	}

	query := "INSERT OR REPLACE INTO vip (jid, name, relation) VALUES (?, ?, ?)"

	_, err = vip.DB.DB.ExecContext(ctx, query, jid, name, rel)

	if err != nil {
		return fmt.Errorf("fail to add new vip: %w", err)
	}

	err = vip.LoadVIP()

	if err != nil {
		return fmt.Errorf("fail to load new vip: %w", err)
	}

	return nil
}

func (vip *VIP) DeleteVIP(input string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if len(input) > 100 {
		return fmt.Errorf("my apologies, Sir, but that entry is far too long to process")
	}

	if input == "" {
		return fmt.Errorf("Input is empty sir! Please input the phone number you want to delete")
	}

	cleaned := strings.TrimPrefix(strings.TrimSpace(input), "+")

	jid, err := sanitizeJID(cleaned)

	if err != nil {
		return err
	}

	query := "DELETE FROM vip WHERE jid = ?"

	_, err = vip.DB.DB.ExecContext(ctx, query, jid)

	if err != nil {
		return fmt.Errorf("fail to add new vip: %w", err)
	}

	err = vip.LoadVIP()

	if err != nil {
		return fmt.Errorf("fail to load new vip: %w", err)
	}

	return nil
}

func (vip *VIP) CheckVIP(jid string) (string, bool) {
	relation, isVIP := vip.VIP[jid]
	if !isVIP {
		return "", false
	}

	return relation, isVIP
}

func sanitizeJID(input string) (string, error) {
	id := strings.Split(input, "@")[0]

	re := regexp.MustCompile(`[^0-9]`)
	id = re.ReplaceAllString(id, "")

	if id == "" {
		return "", fmt.Errorf("JID is empty")
	}

	jid := id + "@s.whatsapp.net"

	return jid, nil
}
