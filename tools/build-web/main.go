// Command build-web bundles the frontend using esbuild's Go library, so the
// whole build is `go generate ./... && go build` with no node_modules.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Gzip budgets. Exceeding one fails the build, which is how "keep it small"
// becomes enforceable rather than aspirational.
// Set to the current actuals plus about 15%.
var budgets = map[string]int{
	"bundle.js":  24_000,
	"bundle.css": 4_000,
}

func main() {
	dev := flag.Bool("dev", false, "unminified output with sourcemaps")
	flag.Parse()

	if err := run(*dev); err != nil {
		fmt.Fprintln(os.Stderr, "build-web:", err)
		os.Exit(1)
	}
}

func run(dev bool) error {
	root, err := findRoot()
	if err != nil {
		return err
	}
	src := filepath.Join(root, "web", "src")
	out := filepath.Join(root, "web", "dist")
	vendor := filepath.Join(root, "web", "vendor")

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	// The vendored dist files import bare specifiers, so they are aliased to
	// the vendor directory rather than resolved through node_modules.
	alias := map[string]string{
		"preact":               filepath.Join(vendor, "preact", "index.js"),
		"preact/hooks":         filepath.Join(vendor, "preact", "hooks", "index.js"),
		"preact/jsx-runtime":   filepath.Join(vendor, "preact", "jsx-runtime", "index.js"),
		"@preact/signals":      filepath.Join(vendor, "signals", "index.js"),
		"@preact/signals-core": filepath.Join(vendor, "signals-core", "index.js"),
	}

	common := api.BuildOptions{
		Bundle:          true,
		Format:          api.FormatIIFE,
		Platform:        api.PlatformBrowser,
		Target:          api.ES2022,
		JSX:             api.JSXAutomatic,
		JSXImportSource: "preact",
		Alias:           alias,
		LogLevel:        api.LogLevelWarning,
		Charset:         api.CharsetUTF8,
		LegalComments:   api.LegalCommentsNone,
		Define: map[string]string{
			"process.env.NODE_ENV": ternary(dev, `"development"`, `"production"`),
		},
	}
	if dev {
		common.Sourcemap = api.SourceMapLinked
	} else {
		common.MinifyWhitespace = true
		common.MinifyIdentifiers = true
		common.MinifySyntax = true
	}

	// The theme boot script is built separately and inlined into <head>, so an
	// explicit light or dark choice applies before first paint and never flashes.
	bootOpts := common
	bootOpts.EntryPoints = []string{filepath.Join(src, "boot-theme.ts")}
	bootOpts.Write = false
	bootOpts.Sourcemap = api.SourceMapNone
	bootOpts.MinifyWhitespace = true
	bootOpts.MinifyIdentifiers = true
	bootOpts.MinifySyntax = true
	bootRes := api.Build(bootOpts)
	if len(bootRes.Errors) > 0 {
		return fmt.Errorf("boot script: %s", formatMessages(bootRes.Errors))
	}
	boot := strings.TrimSpace(string(bootRes.OutputFiles[0].Contents))

	appOpts := common
	appOpts.EntryPoints = []string{
		filepath.Join(src, "main.tsx"),
		filepath.Join(src, "styles", "index.css"),
	}
	appOpts.Outdir = out
	appOpts.Outbase = src
	// Stable names: this is a localhost binary and the Go handler controls
	// caching, so a content hash would only force HTML templating in Go.
	appOpts.EntryNames = "bundle"
	appOpts.Write = true
	appOpts.Metafile = true

	res := api.Build(appOpts)
	if len(res.Errors) > 0 {
		return fmt.Errorf("app bundle: %s", formatMessages(res.Errors))
	}

	if err := writeIndex(src, out, boot); err != nil {
		return err
	}

	fail := false
	for _, name := range []string{"bundle.js", "bundle.css"} {
		path := filepath.Join(out, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		size := gzipSize(raw)
		budget := budgets[name]
		status := "ok"
		if !dev && size > budget {
			status = "OVER BUDGET"
			fail = true
		}
		fmt.Printf("%-12s %6.1f KB raw  %6.1f KB gz  (budget %d KB) %s\n",
			name, kb(len(raw)), kb(size), budget/1024, status)
	}
	if fail {
		return fmt.Errorf("gzip budget exceeded")
	}
	return nil
}

func writeIndex(src, out, boot string) error {
	tpl, err := os.ReadFile(filepath.Join(src, "index.html"))
	if err != nil {
		return fmt.Errorf("read index.html: %w", err)
	}
	html := strings.Replace(string(tpl), "<!--BOOT_THEME-->",
		"<script>"+boot+"</script>", 1)
	return os.WriteFile(filepath.Join(out, "index.html"), []byte(html), 0o644)
}

func gzipSize(b []byte) int {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return buf.Len()
}

func kb(n int) float64 { return float64(n) / 1024 }

func formatMessages(msgs []api.Message) string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Location != nil {
			out = append(out, fmt.Sprintf("%s:%d: %s", m.Location.File, m.Location.Line, m.Text))
			continue
		}
		out = append(out, m.Text)
	}
	return strings.Join(out, "\n")
}

// findRoot walks up to the directory holding go.mod, so the tool works from
// anywhere in the tree.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
