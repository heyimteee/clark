package store

import (
	"strings"
	"testing"
)

func TestProtocolStoreUpsertVersionBump(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	p, err := st.UpsertProtocol(Protocol{Slug: "morning-news", Title: "Morning News", Body: "1. gather\n2. report", Origin: "clark"})
	if err != nil {
		t.Fatalf("UpsertProtocol: %v", err)
	}
	if p.Version != 1 || p.Origin != "clark" || p.UseCount != 0 {
		t.Fatalf("first save = v%d origin %s uses %d, want v1 clark 0", p.Version, p.Origin, p.UseCount)
	}

	p.Origin = "master"
	p2, err := st.UpsertProtocol(p)
	if err != nil {
		t.Fatalf("UpsertProtocol again: %v", err)
	}
	if p2.Version != 2 {
		t.Fatalf("second save version = %d, want 2", p2.Version)
	}
	if p2.ID != p.ID {
		t.Fatalf("upsert created new row: id %d then %d", p.ID, p2.ID)
	}
	list, err := st.ListProtocols()
	if err != nil {
		t.Fatalf("ListProtocols: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListProtocols len = %d, want 1", len(list))
	}
	if list[0].Origin != "master" {
		t.Fatalf("origin after re-save = %s, want master", list[0].Origin)
	}
}

func TestProtocolStoreDefaultsOriginToMaster(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	p, err := st.UpsertProtocol(Protocol{Slug: "x", Title: "X", Body: "steps", Origin: "hacker"})
	if err != nil {
		t.Fatalf("UpsertProtocol: %v", err)
	}
	if p.Origin != "master" {
		t.Fatalf("origin = %q, want master", p.Origin)
	}
}

func TestProtocolStoreBodyCap(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	_, err = st.UpsertProtocol(Protocol{Slug: "big", Title: "Big", Body: strings.Repeat("a", 8<<10+1)})
	if err == nil {
		t.Fatal("UpsertProtocol accepted oversized body")
	}
}

func TestProtocolStoreTouchAndGetDelete(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	p, err := st.UpsertProtocol(Protocol{Slug: "run-report", Title: "Run Report", Body: "1. run"})
	if err != nil {
		t.Fatalf("UpsertProtocol: %v", err)
	}
	if err := st.TouchProtocol(p.ID); err != nil {
		t.Fatalf("TouchProtocol: %v", err)
	}
	got, err := st.GetProtocol("run-report")
	if err != nil {
		t.Fatalf("GetProtocol: %v", err)
	}
	if got.UseCount != 1 || got.LastUsedAt == nil {
		t.Fatalf("after touch: uses %d lastUsed %v, want 1 and set", got.UseCount, got.LastUsedAt)
	}
	if err := st.DeleteProtocol(p.ID); err != nil {
		t.Fatalf("DeleteProtocol: %v", err)
	}
	if _, err := st.GetProtocol("run-report"); err == nil {
		t.Fatal("GetProtocol after delete should fail")
	}
	if err := st.DeleteProtocol(p.ID); err == nil {
		t.Fatal("DeleteProtocol on missing id should fail")
	}
}

func TestProtocolStoreUpsertValidation(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertProtocol(Protocol{Title: "No slug", Body: "b"}); err == nil {
		t.Fatal("empty slug should fail")
	}
	if _, err := st.UpsertProtocol(Protocol{Slug: "no-title", Body: "b"}); err == nil {
		t.Fatal("empty title should fail")
	}
}
