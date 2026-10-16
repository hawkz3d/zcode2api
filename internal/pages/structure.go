package pages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// marshalDetail renders FastAPI shape A {"detail":"..."} in the measured
// compact form.
func marshalDetail(text string) ([]byte, error) {
	return json.Marshal(map[string]string{"detail": text})
}

// ---- structure extraction: the SAME lens the contract baseline used ------
//
// docs/contract/frontend-baseline/README.md: extraction was a pure-regex
// sweep over the template file text (id="..."; class="..." values split on
// whitespace; data-* names; href/src resource references). Our checker must
// reproduce that lens - calibration is proven by TestStructureEquivalence,
// which first re-extracts the LIVE legacy rendering? No: calibration here =
// the extractor reproduces the baseline counts EXACTLY for all four pages
// (ids 1/5/43/27, classes 12/11/98/51, data 0/1/3/2 - golden numbers from
// the baseline statistics tables).

var (
	reID      = regexp.MustCompile(`id="([^"]*)"`)
	reClass   = regexp.MustCompile(`class="([^"]*)"`)
	reData    = regexp.MustCompile(`data-[A-Za-z0-9_-]+`)
	reResrc   = regexp.MustCompile(`(?:href|src)="((?:/static|https://cdn)[^"]*)"`)
	reVersion = regexp.MustCompile(`\{\{APP_VERSION\}\}`)
)

// Extracted is the structural fingerprint of one page.
type Extracted struct {
	IDs     []string
	Classes []string
	Datas   []string
	Resrc   []string
	Text    string // raw text used for substring checks
}

// Extract applies the contract's extraction lens to template text.
func Extract(src string) Extracted {
	ids := uniq(reID.FindAllStringSubmatch(src, -1), 1)
	classes := []string{}
	for _, v := range reClass.FindAllStringSubmatch(src, -1) {
		classes = append(classes, strings.Fields(v[1])...)
	}
	return Extracted{
		IDs:     sortedUniq(ids),
		Classes: sortedUniq(classes),
		Datas:   sortedUniq(reData.FindAllString(src, -1)),
		Resrc:   sortedUniq(reSlice(reResrc.FindAllStringSubmatch(src, -1), 1)),
		Text:    src,
	}
}

func sortedUniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func uniq(matches [][]string, idx int) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[idx])
	}
	return out
}

// re-slice for reResrc: FindAllStringSubmatch returns groups; idx 1.
func reSlice(in [][]string, idx int) []string { return uniq(in, idx) }

// ---- baseline file parsing -----------------------------------------------

// Baseline is one *.structure.md document.
type Baseline struct {
	Name                           string
	Counts                         map[string]int // "id" | "class" | "data" | "resource" -> declared count
	IDs, Classes, Datas, Resources []string
	Texts                          []string // visible-text lines (substring assertions)
}

// blocks maps section titles (lowercased) to their fenced-code content.
func ParseBaseline(path string) (*Baseline, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	bl := &Baseline{Name: filepath.Base(path), Counts: map[string]int{}}
	sections := map[string]string{}
	lines := strings.Split(string(b), "\n")
	var cur string
	var buf []string
	inFence := false
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "```"):
			if inFence {
				sections[cur] = strings.Join(buf, "\n")
				buf = nil
			}
			inFence = !inFence
		case inFence:
			buf = append(buf, ln)
		case strings.HasPrefix(ln, "## "):
			cur = strings.ToLower(strings.TrimPrefix(ln, "## "))
		case strings.HasPrefix(ln, "| ") && inFence == false && cur == "":
			// ignore preamble tables
		}
		if !inFence && strings.Contains(cur, "统计") {
			// capture "| id | **43** |" and "| class（去重） | **98** |" rows
			if m := regexp.MustCompile(`^\|\s*([^|*（]+)(?:（[^）]*）)?\s*\|\s*\*\*(\d+)\*\*\s*\|`).FindStringSubmatch(ln); m != nil {
				n := 0
				fmt.Sscanf(m[2], "%d", &n)
				bl.Counts[strings.TrimSpace(m[1])] = n
			}
		}
	}
	pick := func(needle string) []string {
		for title, body := range sections {
			if strings.Contains(title, needle) {
				return fieldsOf(body)
			}
		}
		return nil
	}
	bl.IDs = pick("id")
	bl.Classes = pick("class")
	bl.Datas = pick("data-")
	bl.Resources = pick("静态资源")
	for title, body := range sections {
		if strings.Contains(title, "可见文本") {
			for _, l := range strings.Split(body, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					bl.Texts = append(bl.Texts, l)
				}
			}
		}
	}
	return bl, nil
}

func fieldsOf(body string) []string {
	var out []string
	for _, ln := range strings.Split(body, "\n") {
		out = append(out, strings.Fields(ln)...)
	}
	sort.Strings(out)
	return out
}

// ---- reporting -------------------------------------------------------------

// Diff reports missing/extra members between template and baseline.
type Diff struct {
	Page                             string
	MissingIDs, ExtraIDs             []string
	MissingClasses, ExtraClasses     []string
	MissingDatas, ExtraDatas         []string
	MissingResources, ExtraResources []string
	MissingTexts                     []string
	CountMismatch                    []string // declared vs actual count mismatches
}

func (d Diff) Clean() bool {
	return len(d.MissingIDs)+len(d.ExtraIDs)+len(d.MissingClasses)+len(d.ExtraClasses)+
		len(d.MissingDatas)+len(d.ExtraDatas)+len(d.MissingResources)+len(d.ExtraResources)+
		len(d.MissingTexts)+len(d.CountMismatch) == 0
}

// Check compares one rendered-authoring template against its baseline.
func Check(page string, tmpl string, bl *Baseline) Diff {
	got := Extract(tmpl)
	d := Diff{Page: page}
	d.MissingIDs, d.ExtraIDs = missingExtra(bl.IDs, got.IDs)
	d.MissingClasses, d.ExtraClasses = missingExtra(bl.Classes, got.Classes)
	d.MissingDatas, d.ExtraDatas = missingExtra(bl.Datas, got.Datas)
	resrcWant := replaceVersionList(bl.Resources, "{{APP_VERSION}}")
	d.MissingResources, d.ExtraResources = missingExtra(resrcWant, got.Resrc)
	// Visible-text checks run on entity-DECODED text: the baseline author
	// extracted text nodes via an HTML parser (which decodes &lt; &gt; &amp;
	// &#39;), while our template source keeps entities verbatim. Without the
	// decode the literal "Authorization: Bearer <key>" could never match the
	// source's "&lt;key&gt;" (calibration: settings page).
	decoded := decodeHTMLEntities(tmpl)
	for _, t := range bl.Texts {
		if !strings.Contains(decoded, t) {
			d.MissingTexts = append(d.MissingTexts, t)
		}
	}
	checkCount(&d, "id", len(bl.IDs), bl.Counts["id"], len(got.IDs))
	checkCount(&d, "class", len(bl.Classes), bl.Counts["class"], len(got.Classes))
	checkCount(&d, "data-*", len(bl.Datas), bl.Counts["data-*"], len(got.Datas))
	checkCount(&d, "resource", len(resrcWant), bl.Counts["静态资源"], len(got.Resrc))
	return d
}

func checkCount(d *Diff, what string, listed, declared, actual int) {
	if declared > 0 && (listed != declared || actual != declared) {
		d.CountMismatch = append(d.CountMismatch,
			fmt.Sprintf("%s: baseline declares %d, listed %d, extracted %d", what, declared, listed, actual))
	}
}

func replaceVersionList(in []string, v string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ReplaceAll(s, "2.5.1", v))
	}
	sort.Strings(out)
	return out
}

func missingExtra(want, have []string) (missing, extra []string) {
	w := set(want)
	h := set(have)
	for k := range w {
		if !h[k] {
			missing = append(missing, k)
		}
	}
	for k := range h {
		if !w[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return
}

func set(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

// htmlEscapes covers the entities the legacy templates use (measured sweep
// over the four pages: &lt; &gt; &amp; &quot; &#39;).
var htmlEscapes = strings.NewReplacer(
	"&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&apos;", "'",
	"&nbsp;", "\u00a0", "&amp;", "&", // &amp; last: unescapes remnants of the others
)

func decodeHTMLEntities(s string) string { return htmlEscapes.Replace(s) }

// ---- path resolution for the baseline directory ----------------------------

// BaselineDir locates docs/contract/frontend-baseline relative to this
// source file so the checker runs from any working directory.
func BaselineDir() (string, error) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("pages: cannot locate source path")
	}
	root := filepath.Join(filepath.Dir(self), "..", "..")
	dir := filepath.Join(root, "docs", "contract", "frontend-baseline")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("pages: baseline dir not found at %s", dir)
	}
	return dir, nil
}

var _ = reSlice // kept for group-index flexibility if patterns evolve
