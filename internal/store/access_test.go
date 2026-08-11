package store

import "testing"

func TestAccessRoundTrip(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := st

	tools, ok, err := s.GetTools("6281267858909")
	if err != nil {
		t.Fatalf("GetTools on empty: %v", err)
	}
	if ok {
		t.Fatal("no access row should exist yet")
	}
	if len(tools) != 0 {
		t.Errorf("tools = %v, want none", tools)
	}

	if err := s.SetTools("6281267858909", []string{"web_search"}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}
	tools, ok, err = s.GetTools("6281267858909")
	if err != nil {
		t.Fatalf("GetTools: %v", err)
	}
	if !ok || len(tools) != 1 || tools[0] != "web_search" {
		t.Errorf("tools = %v (ok=%v), want [web_search]", tools, ok)
	}

	if err := s.SetTools("6281267858909", []string{}); err != nil {
		t.Fatalf("SetTools empty: %v", err)
	}
	tools, ok, err = s.GetTools("6281267858909")
	if err != nil {
		t.Fatalf("GetTools: %v", err)
	}
	if !ok || len(tools) != 0 {
		t.Errorf("tools = %v (ok=%v), want empty revoke", tools, ok)
	}

	if err := s.DeleteAccess("6281267858909"); err != nil {
		t.Fatalf("DeleteAccess: %v", err)
	}
	if _, ok, err := s.GetTools("6281267858909"); err != nil || ok {
		t.Fatalf("after delete: ok=%v err=%v", ok, err)
	}
}

func TestDeleteVIPCascadesAccess(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := st

	if err := s.Add(VIPEntry{JID: "6281267858909", Name: "Tiara", Relation: "Girlfriend"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.SetTools("6281267858909", []string{"web_search"}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}

	if err := s.Delete("6281267858909"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := s.GetTools("6281267858909"); err != nil || ok {
		t.Fatalf("access row should be gone after VIP delete: ok=%v err=%v", ok, err)
	}
}
