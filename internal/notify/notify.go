// Package notify delivers desktop notifications.
package notify

import (
	_ "embed"

	"github.com/gen2brain/beeep"
)

//go:embed clark.png
var icon []byte

// Beeep posts notifications through the beeep library.
type Beeep struct{}

// New returns a Beeep notifier configured for clark.
func New() *Beeep {
	beeep.AppName = "Clark"
	return &Beeep{}
}

// Notify shows a desktop notification with clark's icon.
func (Beeep) Notify(title, body string) error {
	return beeep.Notify(title, body, icon)
}
