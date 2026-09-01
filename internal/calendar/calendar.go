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
	Location string    `json:"location,omitempty"`
	Notes    string    `json:"notes,omitempty"`
}

type Client interface {
	List(ctx context.Context, from, to time.Time) ([]Event, error)
	Create(ctx context.Context, e Event) (string, error)
	Delete(ctx context.Context, id string) error
}
