package assistant

import (
	"testing"
)

// TestAddVIPBulkAllOrNothing verifies the web bulk-add path reuses the v3.1
// semantics: a bad line in the middle rejects the whole batch, and a valid
// batch lands every entry.
func TestAddVIPBulkAllOrNothing(t *testing.T) {
	s, _, _ := newService(t)

	entries := []string{
		"6281111111111,Alice,Sister",
		"6282222222222,Bob,Friend",
	}
	if err := s.AddVIPBulk(entries); err != nil {
		t.Fatalf("AddVIPBulk valid: %v", err)
	}
	vips := s.VIPList()
	if len(vips) != 2 {
		t.Fatalf("VIPList after bulk = %d, want 2", len(vips))
	}
	if _, ok := s.vip.Check("6281111111111@s.whatsapp.net"); !ok {
		t.Error("Alice missing after bulk add")
	}

	// A malformed entry must leave the whole batch unwritten.
	bad := []string{"6283333333333,Carl,Uncle", "this is not valid"}
	if err := s.AddVIPBulk(bad); err == nil {
		t.Fatal("AddVIPBulk accepted a malformed batch")
	}
	if _, ok := s.vip.Check("6283333333333@s.whatsapp.net"); ok {
		t.Error("Carl was written despite the malformed batch (not all-or-nothing)")
	}
}

// TestAddVIPBulkEmptyRejected guards the bulk endpoint against blank input.
func TestAddVIPBulkEmptyRejected(t *testing.T) {
	s, _, _ := newService(t)
	if err := s.AddVIPBulk(nil); err == nil {
		t.Error("AddVIPBulk(nil) accepted, want error")
	}
	if err := s.AddVIPBulk([]string{"", "  "}); err == nil {
		t.Error("AddVIPBulk(blank lines) accepted, want error")
	}
}
