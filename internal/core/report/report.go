// Package report renders results.json and the self-contained report.html
// (docs/05). report.html is the primary human-facing deliverable; results.json
// is its data source.
package report

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/felenko/uitest/internal/core/result"
)

//go:embed assets/report.css assets/report.js assets/report.html.tmpl
var assets embed.FS

// view is the template model: the results plus rendering helpers.
type view struct {
	*result.Results
	Embed     bool
	InlineCSS template.CSS
	InlineJS  template.JS
	Generated string
	imgData   map[string]template.URL
}

func (v *view) IsPass() bool {
	return v.Summary.Failed == 0 && v.Summary.Errors == 0
}

func (v *view) DurationMs() int64 {
	return v.FinishedAt.Sub(v.StartedAt).Milliseconds()
}

// Img resolves an artifact path to either a relative URL or an embedded data URI.
func (v *view) Img(rel string) template.URL {
	if rel == "" {
		return ""
	}
	if v.Embed {
		if d, ok := v.imgData[rel]; ok {
			return d
		}
	}
	return template.URL(rel)
}

type stepsArgs struct {
	Root  *view
	Steps []result.Step
}

var funcs = template.FuncMap{
	"args": func(root *view, steps []result.Step) stepsArgs { return stepsArgs{Root: root, Steps: steps} },
	"ts":   func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"dur":  formatDuration,
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// WriteAll writes results.json, summary.txt, the report assets, and report.html
// into outDir. With embed, CSS/JS/images are inlined into a single file.
func WriteAll(outDir string, res *result.Results, embed bool) (string, error) {
	if err := writeJSON(outDir, res); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.txt"), []byte(Summarize(res)), 0o644); err != nil {
		return "", err
	}

	v := &view{Results: res, Embed: embed, Generated: time.Now().Local().Format("2006-01-02 15:04:05")}

	if embed {
		css, _ := assets.ReadFile("assets/report.css")
		js, _ := assets.ReadFile("assets/report.js")
		v.InlineCSS = template.CSS(css)
		v.InlineJS = template.JS(js)
		v.imgData = embedImages(outDir, res)
	} else {
		if err := copyAsset(outDir, "report.css"); err != nil {
			return "", err
		}
		if err := copyAsset(outDir, "report.js"); err != nil {
			return "", err
		}
	}

	tmpl, err := template.New("report.html.tmpl").Funcs(funcs).ParseFS(assets, "assets/report.html.tmpl")
	if err != nil {
		return "", err
	}
	reportPath := filepath.Join(outDir, "report.html")
	f, err := os.Create(reportPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := tmpl.Execute(f, v); err != nil {
		return "", err
	}
	return reportPath, nil
}

func writeJSON(outDir string, res *result.Results) error {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "results.json"), data, 0o644)
}

func copyAsset(outDir, name string) error {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "assets", name), data, 0o644)
}

// embedImages reads every referenced screenshot and returns base64 data URIs.
func embedImages(outDir string, res *result.Results) map[string]template.URL {
	out := map[string]template.URL{}
	add := func(rel string) {
		if rel == "" {
			return
		}
		if _, ok := out[rel]; ok {
			return
		}
		data, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(rel)))
		if err != nil {
			return
		}
		out[rel] = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(data))
	}
	for _, c := range res.Cases {
		for _, phase := range [][]result.Step{c.Setup, c.Steps, c.Teardown} {
			for _, s := range phase {
				for _, sh := range s.Screenshots {
					add(sh)
				}
			}
		}
		for _, a := range c.Validation.Assert {
			add(a.Expected.Image)
			add(a.Actual.Image)
		}
	}
	return out
}

// Summarize returns a short plain-text rollup for run.log / stdout.
func Summarize(res *result.Results) string {
	var b strings.Builder
	verdict := "PASS"
	if res.Summary.Failed > 0 || res.Summary.Errors > 0 {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "%s — %s\n", verdict, res.Session)
	fmt.Fprintf(&b, "  total=%d passed=%d failed=%d errors=%d skipped=%d\n",
		res.Summary.Total, res.Summary.Passed, res.Summary.Failed,
		res.Summary.Errors, res.Summary.Skipped)
	for _, c := range res.Cases {
		fmt.Fprintf(&b, "  [%s] %s — %s\n", strings.ToUpper(string(c.Status)), c.ID, c.Name)
	}
	return b.String()
}
