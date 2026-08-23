package assistant

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/heyimteee/clark/internal/logging"
	"github.com/heyimteee/clark/internal/store"
)

// vipInputRe matches one "number, name, relation" entry. The number accepts the
// natural formatting people type — a leading +, spaces, dashes, parentheses and
// dots — while name and relation stay letters (and spaces) only.
var vipInputRe = regexp.MustCompile(`^[0-9+][0-9+()\s.\-]{0,29}\s*,\s*[\p{L}\s]{1,50}\s*,\s*[\p{L}\s]{1,50}$`)

// numberedEntryRe matches one line of a bulk VIP list: "N. <entry>" or "N) <entry>".
var numberedEntryRe = regexp.MustCompile(`(?i)^\s*\d+[.)]\s*(.+)$`)

// VIP manages the inner circle: validation, persistence, and in-memory lookups.
type VIP struct {
	store   store.VIPStore
	entries map[string]string
	byName  map[string]string
	enabled map[string]bool
}

// NewVIP returns an empty VIP backed by the given store.
func NewVIP(s store.VIPStore) *VIP {
	return &VIP{
		store:   s,
		entries: make(map[string]string),
		byName:  make(map[string]string),
		enabled: make(map[string]bool),
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
	v.enabled = make(map[string]bool, len(entries))
	for _, e := range entries {
		label := fmt.Sprintf("%v (%v)", e.Name, e.Relation)
		v.entries[e.JID] = label
		v.byName[strings.ToLower(e.Name)] = e.JID
		v.byName[strings.ToLower(label)] = e.JID

		on, ok, err := v.store.Enabled(e.JID)
		if err != nil {
			return err
		}
		if ok {
			v.enabled[e.JID] = on
		}
	}

	if len(v.entries) < 1 {
		logging.Log("MEMORY", logging.SevNotice, "VIPLOAD", "VIP list is empty")
	}
	return nil
}

// Lookup resolves a name or phone number to a VIP jid. Phone numbers may be
// written naturally — "+62 821-7450-0836", "0821-7450-0836" — and are
// normalized to digits before matching.
func (v *VIP) Lookup(input string) (string, bool) {
	cleaned := strings.TrimSpace(input)
	if jid, ok := v.byName[strings.ToLower(cleaned)]; ok {
		return jid, true
	}

	id := strings.Split(cleaned, "@")[0]
	if digits := digitsOnly(id); digits != "" {
		target := digits + "@s.whatsapp.net"
		if _, ok := v.entries[target]; ok {
			return target, true
		}
	}
	return "", false
}

// nonDigitRe strips everything but digits; package-level so the hot inbound
// path never recompiles it.
var nonDigitRe = regexp.MustCompile(`[^0-9]`)

// digitsOnly keeps the digits of a phone-ish string, dropping +, spaces, and
// punctuation, so "0821-7450-0836" and "+62 821-7450-0836" both yield digits.
func digitsOnly(s string) string {
	return nonDigitRe.ReplaceAllString(s, "")
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

// IsEnabled reports a VIP's status override. hasOverride is false when the
// VIP follows the global status instead of a personal carve-out.
func (v *VIP) IsEnabled(jid string) (on bool, hasOverride bool) {
	on, ok := v.enabled[jid]
	return on, ok
}

// SetEnabled persists a VIP's status override and refreshes the cache.
func (v *VIP) SetEnabled(jid string, on bool) error {
	if _, ok := v.entries[jid]; !ok {
		return fmt.Errorf("no VIP found matching %q", jid)
	}
	if err := v.store.SetEnabled(jid, on); err != nil {
		return err
	}
	v.enabled[jid] = on
	return nil
}

// EnabledMap returns every VIP's status override keyed by jid.
func (v *VIP) EnabledMap() map[string]bool {
	out := make(map[string]bool, len(v.enabled))
	for jid, on := range v.enabled {
		out[jid] = on
	}
	return out
}

// ClearEnabled removes a VIP's status override and refreshes the cache.
func (v *VIP) ClearEnabled(jid string) error {
	if err := v.store.ClearEnabled(jid); err != nil {
		return err
	}
	delete(v.enabled, jid)
	return nil
}

// ClearAllEnabled removes every VIP's status override and refreshes the cache.
func (v *VIP) ClearAllEnabled() error {
	if err := v.store.ClearAllEnabled(); err != nil {
		return err
	}
	v.enabled = make(map[string]bool)
	return nil
}

// Add parses and persists a "[number], [name], [relation]" entry, or a numbered
// list of several such entries, then reloads. Bulk lists are validated in full
// before any entry is written, so a bad line never leaves a partial state.
func (v *VIP) Add(input string) error {
	if len(input) > 500 {
		return fmt.Errorf("my apologies, Sir, but that entry is far too long to process")
	}

	if input == "" {
		return fmt.Errorf("Input is empty sir! Format: [number], [name], [relation]")
	}

	entries, ok := parseBulkVIP(input)
	if !ok {
		return v.addSingle(input)
	}

	type parsed struct {
		jid, name, relation string
	}
	all := make([]parsed, 0, len(entries))
	for _, e := range entries {
		jid, name, relation, err := v.parseEntry(e)
		if err != nil {
			return fmt.Errorf("entry %q: %w", e, err)
		}
		all = append(all, parsed{jid: jid, name: name, relation: relation})
	}
	for _, p := range all {
		if err := v.store.Add(store.VIPEntry{JID: p.jid, Name: p.name, Relation: p.relation}); err != nil {
			return err
		}
	}
	return v.Load()
}

// addSingle validates and persists one "[number], [name], [relation]" entry.
func (v *VIP) addSingle(input string) error {
	jid, name, relation, err := v.parseEntry(input)
	if err != nil {
		return err
	}

	if err := v.store.Add(store.VIPEntry{JID: jid, Name: name, Relation: relation}); err != nil {
		return err
	}
	return v.Load()
}

// parseEntry validates a "[number], [name], [relation]" payload and returns its
// normalized pieces. It never touches the store.
func (v *VIP) parseEntry(input string) (jid, name, relation string, err error) {
	if len(input) > 100 {
		return "", "", "", fmt.Errorf("my apologies, Sir, but that entry is far too long to process")
	}

	if input == "" {
		return "", "", "", fmt.Errorf("Input is empty sir! Format: [number], [name], [relation]")
	}

	cleaned := strings.TrimPrefix(strings.TrimSpace(input), "+")
	if !vipInputRe.MatchString(cleaned) {
		return "", "", "", fmt.Errorf("forgive me, Sir, the format is invalid. " +
			"Please use: [Number], [Relation]. The name should be letters only")
	}

	parts := strings.Split(input, ",")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("my apologies Sir, I require exactly three details: Number, Name, Relation")
	}

	number := strings.TrimSpace(parts[0])
	name = strings.TrimSpace(parts[1])
	relation = strings.TrimSpace(parts[2])
	if number == "" || name == "" || relation == "" {
		return "", "", "", fmt.Errorf("my apologies Sir. Number, name, and relation are required")
	}

	jid, err = sanitizeJID(number)
	if err != nil {
		return "", "", "", err
	}
	return jid, name, relation, nil
}

// parseBulkVIP splits a numbered list like "1. X, Y, Z\n2. A, B, C" into its
// per-entry payloads, stripping the numbering prefix. A single numbered line is
// also accepted; ok is false when the input is not a numbered list at all.
func parseBulkVIP(input string) ([]string, bool) {
	var entries []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := numberedEntryRe.FindStringSubmatch(line)
		if m == nil {
			return nil, false
		}
		entries = append(entries, strings.TrimSpace(m[1]))
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
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

// Clear empties the entire inner circle and reloads.
func (v *VIP) Clear() error {
	if err := v.store.ClearAll(); err != nil {
		return err
	}
	return v.Load()
}

func sanitizeJID(input string) (string, error) {
	id := strings.Split(input, "@")[0]
	id = digitsOnly(id)

	if id == "" {
		return "", fmt.Errorf("JID is empty")
	}

	return id + "@s.whatsapp.net", nil
}
