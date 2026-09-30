package calendar

import (
	"context"
	"time"
)

type Event struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	AllDay   bool      `json:"allDay,omitempty"`
	Location string    `json:"location,omitempty"`
	Notes    string    `json:"notes,omitempty"`
	// Recurring marks an occurrence that came from a repeating series.
	Recurring bool `json:"recurring,omitempty"`
	// OccurrenceID addresses a single date within a series, in the form
	// "<uid>:<RFC3339>". Passing it to Delete cancels just that date via
	// EXDATE, leaving the rest of the series intact.
	OccurrenceID string `json:"occurrenceId,omitempty"`
	// CalendarHref selects the destination collection for Create. Empty means the
	// configured default, or the first discovered collection.
	CalendarHref string `json:"calendarHref,omitempty"`
}

type Client interface {
	List(ctx context.Context, from, to time.Time) ([]Event, error)
	Create(ctx context.Context, e Event) (string, error)
	Delete(ctx context.Context, id string) error
}
