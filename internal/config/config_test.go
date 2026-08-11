package config

import "testing"

func TestParsePeopleFull(t *testing.T) {
	people := parsePeople("Tiara|Girlfriend;Anang|Father; Renni | Mother ;Aziz|Bestfriend")
	if len(people) != 4 {
		t.Fatalf("got %d people, want 4: %v", len(people), people)
	}
	want := []Person{
		{Name: "Tiara", Relation: "Girlfriend"},
		{Name: "Anang", Relation: "Father"},
		{Name: "Renni", Relation: "Mother"},
		{Name: "Aziz", Relation: "Bestfriend"},
	}
	for i, w := range want {
		if people[i] != w {
			t.Errorf("people[%d] = %+v, want %+v", i, people[i], w)
		}
	}
}

func TestParsePeopleRelationless(t *testing.T) {
	people := parsePeople("Tiara;Anang")
	if len(people) != 2 {
		t.Fatalf("got %d people, want 2: %v", len(people), people)
	}
	if people[0] != (Person{Name: "Tiara"}) {
		t.Errorf("people[0] = %+v, want Tiara with no relation", people[0])
	}
}

func TestParsePeopleEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", ";;", "|;|"} {
		if people := parsePeople(raw); len(people) != 0 {
			t.Errorf("parsePeople(%q) = %v, want none", raw, people)
		}
	}
}
