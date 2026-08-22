//go:build darwin

package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// Native uses osascript, which ships with macOS. terminal-notifier would give
// richer notifications but is not installed by default.
type Native struct{ path string }

func NewNative() Sink {
	path, err := exec.LookPath("osascript")
	if err != nil {
		return &Native{}
	}
	return &Native{path: path}
}

func (n *Native) Name() string    { return "native" }
func (n *Native) Available() bool { return n.path != "" }

func (n *Native) Notify(ctx context.Context, a snapshot.Alert) error {
	if !n.Available() {
		return fmt.Errorf("osascript not found in PATH")
	}
	script := fmt.Sprintf(
		"display notification %s with title %s",
		quote(body(a)), quote(title(a)))

	cmd := exec.CommandContext(ctx, n.path, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %w: %s", err, out)
	}
	return nil
}

// quote builds an AppleScript string literal. Alert text contains coin names and
// user-authored messages, so escaping is not optional.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}
