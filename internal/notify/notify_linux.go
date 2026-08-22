//go:build linux

package notify

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// Native uses notify-send, which every freedesktop notification daemon provides
// and which handles the D-Bus session lookup itself. Speaking D-Bus directly
// would mean a dependency or hand-rolling the wire protocol for no gain.
type Native struct{ path string }

func NewNative() Sink {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return &Native{}
	}
	return &Native{path: path}
}

func (n *Native) Name() string    { return "native" }
func (n *Native) Available() bool { return n.path != "" }

func (n *Native) Notify(ctx context.Context, a snapshot.Alert) error {
	if !n.Available() {
		return fmt.Errorf("notify-send not found in PATH")
	}
	urgency := "normal"
	switch a.Severity {
	case "critical":
		urgency = "critical"
	case "info":
		urgency = "low"
	}
	// A stable app name groups the notifications; a per-rule replace id would
	// hide a second alert behind the first.
	cmd := exec.CommandContext(ctx, n.path,
		"--app-name=lcw-dashboard",
		"--urgency="+urgency,
		title(a), body(a))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify-send: %w: %s", err, out)
	}
	return nil
}
