package store

import "testing"

func TestScheduleStoreCRUD(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	sc, err := st.UpsertSchedule(Schedule{Name: "news", Task: "gather", Spec: "0 6 * * *", Enabled: true})
	if err != nil {
		t.Fatalf("UpsertSchedule: %v", err)
	}
	if sc.ID == 0 || !sc.Enabled {
		t.Fatalf("created schedule wrong: %+v", sc)
	}

	sc.Task = "gather and report"
	sc.Enabled = false
	up, err := st.UpsertSchedule(sc)
	if err != nil {
		t.Fatalf("UpsertSchedule update: %v", err)
	}
	if up.ID != sc.ID || up.Enabled {
		t.Fatalf("update wrong: %+v", up)
	}

	list, err := st.ListSchedules()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSchedules: %v len=%d", err, len(list))
	}

	if err := st.SetScheduleEnabled(sc.ID, true); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}
	got, err := st.GetSchedule("news")
	if err != nil || !got.Enabled {
		t.Fatalf("GetSchedule: %v enabled=%v", err, got.Enabled)
	}

	if err := st.MarkScheduleRun(sc.ID); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}
	got, err = st.GetSchedule("news")
	if err != nil || got.LastRunAt == nil {
		t.Fatalf("after mark: %v lastRun=%v", err, got.LastRunAt)
	}

	if err := st.DeleteSchedule(sc.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if err := st.DeleteSchedule(sc.ID); err == nil {
		t.Fatal("delete missing id should fail")
	}
	if _, err := st.GetSchedule("news"); err == nil {
		t.Fatal("GetSchedule after delete should fail")
	}
}

func TestScheduleStoreValidation(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertSchedule(Schedule{Task: "t", Spec: "* * * * *", Enabled: true}); err == nil {
		t.Fatal("empty name should fail")
	}
}
