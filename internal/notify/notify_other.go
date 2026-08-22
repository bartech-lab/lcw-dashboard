//go:build !linux && !darwin

package notify

import (
	"context"
	"fmt"

	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// Native is a no-op on platforms without a supported notifier, so the alerts
// feature degrades to the browser and log sinks rather than failing to build.
type Native struct{}

func NewNative() Sink { return &Native{} }

func (n *Native) Name() string    { return "native" }
func (n *Native) Available() bool { return false }

func (n *Native) Notify(context.Context, snapshot.Alert) error {
	return fmt.Errorf("no native notifier on this platform")
}
