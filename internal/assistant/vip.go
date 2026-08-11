package assistant

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/store"
)

var vipInputRe = regexp.MustCompile(`^[0-9]{1,15}\s*,\s*[\p{L}\s]{1,50}\s*,\s*[\p{L}\s]{1,50}$`)

// VIP manages the inner circle: validation, persistence, and in-memory lookups.
type VIP struct {
	store   store.VIPStore
	entries map[string]string
	byName  map[string]string
}

// NewVIP returns an empty VIP backed by the given store.
func NewVIP(s store.VIPStore) *VIP {
	return &VIP{
		store:   s,
		entries: make(map[string]string),
		byName:  make(map[string]string),
	}
}

// Load refreshes the in-memory lookup tables from the store.
func (v *VIP) Load() error {
	entries, err := v.store.All()
	if err != nil {
		return err
	}

	v.entries = make(map[string]string, len(entries))
	v.byName = make(map[string]string, len(entries))
	for _, e := range entries {
		label := fmt.Sprintf("%v (%v)", e.Name, e.Relation)
		v.entries[e.JID] = label
		v.byName[strings.ToLower(e.Name)] = e.JID
		v.byName[strings.ToLower(label)] = e.JID
	}

	if len(v.entries) < 1 {
		logging.Log("MEMORY", logging.SevNotice, "VIPLOAD", "VIP list is empty")
	}
	return nil
}

// Lookup resolves a name or phone number to a VIP jid.
func (v *VIP) Lookup(input string) (string, bool) {
	cleaned := strings.TrimPrefix(strings.TrimSpace(input), "+")
	if jid, ok := v.byName[strings.ToLower(cleaned)]; ok {
		return jid, true
	}

	id := strings.Split(cleaned, "@")[0]
	if id != "" && isDigits(id) {
		target := id + "@s.whatsapp.net"
		if _, ok := v.entries[target]; ok {
			return target, true
		}
	}
	return "", false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Check resolves a jid to its "Name (Relation)" label.
func (v *VIP) Check(jid string) (string, bool) {
	relation, ok := v.entries[jid]
	return relation, ok
}

// List returns the current entries keyed by jid.
func (v *VIP) List() map[string]string {
	return v.entries
}

func (v *VIP) list() string {
	parts := make([]string, 0, len(v.entries))
	for _, relation := range v.entries {
		parts = append(parts, relation)
	}
	if len(parts) == 0 {
		return "None"
	}
	return strings.Join(parts, ", ")
}

// Add parses and persists a "[number], [name], [relation]" entry, then reloads.
func (v *VIP) Add(input string) error {
	if len(input) > 100 {
		return fmt.Errorf("my apologies, Sir, but that entry is far too long to process")
	}

	if input == "" {
		return fmt.Errorf("Input is empty sir! Format: [number], [name], [relation]")
	}

	cleaned := strings.TrimPrefix(strings.TrimSpace(input), "+")
	if !vipInputRe.MatchString(cleaned) {
		return fmt.Errorf("forgive me, Sir, the format is invalid. " +
			"Please use: [Number], [Relation]. The name should be letters only")
	}

	parts := strings.Split(input, ",")
	if len(parts) != 3 {
		return fmt.Errorf("my apologies Sir, I require exactly three details: Number, Name, Relation")
	}

	number := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	rel := strings.TrimSpace(parts[2])
	if number == "" || name == "" || rel == "" {
		return fmt.Errorf("my apologies Sir. Number, name, and relation are required")
	}

	jid, err := sanitizeJID(number)
	if err != nil {
		return err
	}

	if err := v.store.Add(store.VIPEntry{JID: jid, Name: name, Relation: rel}); err != nil {
		return err
	}
	return v.Load()
}

// Delete removes a VIP by number and reloads.
func (v *VIP) Delete(input string) error {
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

	if err := v.store.Delete(jid); err != nil {
		return err
	}
	return v.Load()
}

func sanitizeJID(input string) (string, error) {
	id := strings.Split(input, "@")[0]
	id = regexp.MustCompile(`[^0-9]`).ReplaceAllString(id, "")

	if id == "" {
		return "", fmt.Errorf("JID is empty")
	}

	return id + "@s.whatsapp.net", nil
}
