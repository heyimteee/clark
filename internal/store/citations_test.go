package store

import (
	"fmt"
	"testing"
)

func TestCitationsRecordAndList(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	pairs := [][2]string{{"Paris Guide", "https://a.example/p"}, {"Rome Guide", "https://b.example/r"}}
	if err := st.RecordCitations("master@master", "europe trip", pairs); err != nil {
		t.Fatalf("RecordCitations: %v", err)
	}
	rows, err := st.ListCitations("master@master", "", 10)
	if err != nil {
		t.Fatalf("ListCitations: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list len = %d, want 2", len(rows))
	}
	if rows[0].URL != "https://b.example/r" {
		t.Fatalf("newest first: %s", rows[0].URL)
	}
	if rows[0].Query != "europe trip" || rows[0].Title != "Rome Guide" {
		t.Fatalf("row fields wrong: %+v", rows[0])
	}

	// Same URL re-saved with a new query: one row, refreshed fields.
	if err := st.RecordCitations("master@master", "italy", [][2]string{{"Rome Guide v2", "https://b.example/r"}}); err != nil {
		t.Fatalf("RecordCitations upsert: %v", err)
	}
	rows, err = st.ListCitations("master@master", "", 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("after upsert: %v len=%d", err, len(rows))
	}
	if rows[0].URL != "https://b.example/r" || rows[0].Query != "italy" || rows[0].Title != "Rome Guide v2" {
		t.Fatalf("upsert did not refresh: %+v", rows[0])
	}

	// Query filter matches title/url/query.
	rows, err = st.ListCitations("master@master", "paris", 10)
	if err != nil || len(rows) != 1 || rows[0].URL != "https://a.example/p" {
		t.Fatalf("filter: %v %+v", err, rows)
	}
	rows, err = st.ListCitations("master@master", "", 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("limit: %v len=%d", err, len(rows))
	}
}

func TestCitationsTTLPurge(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := st.RecordCitations("web", "old news", [][2]string{{"Old", "https://o.example/"}}); err != nil {
		t.Fatalf("RecordCitations: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE citations SET created_at = datetime('now', '-49 hours')`); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Expired rows are invisible to reads even before the next purge.
	rows, err := st.ListCitations("web", "", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("expired visible: %v len=%d", err, len(rows))
	}
	// A fresh record purges the expired row physically.
	if err := st.RecordCitations("web", "fresh", [][2]string{{"New", "https://n.example/"}}); err != nil {
		t.Fatalf("RecordCitations: %v", err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM citations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("purge: %v count=%d", err, n)
	}
}

func TestCitationsCapTrim(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	batch := make([][2]string, 0, 210)
	for i := 0; i < 210; i++ {
		batch = append(batch, [2]string{fmt.Sprintf("T%d", i), fmt.Sprintf("https://x.example/%d", i)})
	}
	if err := st.RecordCitations("web", "bulk", batch); err != nil {
		t.Fatalf("RecordCitations: %v", err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM citations`).Scan(&n); err != nil || n != 200 {
		t.Fatalf("cap: %v count=%d, want 200", err, n)
	}
	rows, err := st.ListCitations("web", "", 10)
	if err != nil {
		t.Fatalf("ListCitations: %v", err)
	}
	if rows[0].URL != "https://x.example/209" {
		t.Fatalf("newest kept wrong: %s", rows[0].URL)
	}
}

func TestCitationsValidation(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := st.RecordCitations("", "q", [][2]string{{"T", "https://x.example/"}}); err == nil {
		t.Fatal("empty jid should fail")
	}
	// Blank URLs are skipped, not fatal.
	if err := st.RecordCitations("web", "q", [][2]string{{"T", ""}}); err != nil {
		t.Fatalf("blank url: %v", err)
	}
}
