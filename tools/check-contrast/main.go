// Command check-contrast measures the delta and text colours in tokens.css
// against their own surface, so a colour change cannot silently fall below AA.
package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
)

// 4.5:1 is the AA floor for the 13px text these colours are used on.
const floor = 4.5

func main() {
	raw, err := os.ReadFile("web/src/styles/tokens.css")
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-contrast:", err)
		os.Exit(1)
	}
	css := string(raw)

	checks := []struct{ token, surface, theme string }{
		{"--delta-up", "#fcfcfb", "light"},
		{"--delta-down", "#fcfcfb", "light"},
		{"--text-secondary", "#fcfcfb", "light"},
		{"--text-muted", "#fcfcfb", "light"},
		{"--delta-up", "#1a1a19", "dark"},
		{"--delta-down", "#1a1a19", "dark"},
		{"--text-secondary", "#1a1a19", "dark"},
		{"--text-muted", "#1a1a19", "dark"},
	}

	failed := false
	for _, c := range checks {
		col, ok := value(css, c.token, c.theme)
		if !ok {
			fmt.Printf("%-18s %-6s NOT FOUND\n", c.token, c.theme)
			failed = true
			continue
		}
		r := ratio(col, c.surface)
		status := "ok"
		if r < floor {
			status = "BELOW AA"
			failed = true
		}
		fmt.Printf("%-18s %-6s %s on %s  %5.2f:1  %s\n",
			c.token, c.theme, col, c.surface, r, status)
	}
	if failed {
		os.Exit(1)
	}
}

// value reads a token from the light block (everything before the first dark
// scope) or from the dark block.
func value(css, token, theme string) (string, bool) {
	section := css
	if at := regexp.MustCompile(`prefers-color-scheme: dark`).FindStringIndex(css); at != nil {
		if theme == "light" {
			section = css[:at[0]]
		} else {
			section = css[at[0]:]
		}
	}
	m := regexp.MustCompile(regexp.QuoteMeta(token) + `:\s*(#[0-9a-fA-F]{6})`).FindStringSubmatch(section)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func ratio(a, b string) float64 {
	la, lb := lum(a), lum(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

func lum(hex string) float64 {
	ch := make([]float64, 3)
	for i := 0; i < 3; i++ {
		v, _ := strconv.ParseInt(hex[1+i*2:3+i*2], 16, 32)
		c := float64(v) / 255
		if c <= 0.03928 {
			ch[i] = c / 12.92
		} else {
			ch[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
}
