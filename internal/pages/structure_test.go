package pages

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestStructureEquivalence is the ADR-006 option-C self-check the t9 task
// demands: re-extract each authored template with the SAME regex lens the
// contract baseline used, compare against docs/contract/frontend-baseline,
// and print a full missing/extra report. A non-clean diff FAILS the test
// (the diff is the evidence, printed either way).
func TestStructureEquivalence(t *testing.T) {
	dir, err := BaselineDir()
	if err != nil {
		t.Skipf("baseline docs not reachable from this checkout: %v", err)
	}
	pages := Pages()
	sort.Strings(pages)
	var report strings.Builder
	any := false
	for _, p := range pages {
		bl, err := ParseBaseline(filepath.Join(dir, baselineFile(p)))
		if err != nil {
			t.Fatalf("parse baseline for %s: %v", p, err)
		}
		tmpl, _ := Template(p)
		d := Check(p, tmpl, bl)
		printDiff(&report, d)
		if !d.Clean() {
			any = true
		}
	}
	t.Log("\n" + report.String())
	if any {
		t.Error("structure drift vs contract baseline (see report above)")
	}
}

// TestExtractorCalibration proves our extractor reproduces the baseline's
// OWN declared statistics exactly - i.e. we are looking through the same
// lens the contract author used, not a stricter/looser one. This is what
// licenses the equivalence claim in TestStructureEquivalence.
func TestExtractorCalibration(t *testing.T) {
	dir, err := BaselineDir()
	if err != nil {
		t.Skipf("baseline docs not reachable: %v", err)
	}
	// Golden declared counts from the four baseline statistics tables.
	golden := map[string]struct{ ids, classes, datas int }{
		"/admin/login":      {1, 12, 0},
		"/admin/settings":   {5, 11, 1},
		"/admin/accounts":   {43, 98, 3},
		"/admin/monitoring": {27, 51, 2},
	}
	for _, p := range []string{"/admin/login", "/admin/settings", "/admin/accounts", "/admin/monitoring"} {
		bl, err := ParseBaseline(filepath.Join(dir, baselineFile(p)))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		tmpl, _ := Template(p)
		got := Extract(tmpl)
		g := golden[p]
		if len(got.IDs) != g.ids || len(got.Classes) != g.classes || len(got.Datas) != g.datas {
			t.Errorf("%s: extracted (id=%d class=%d data=%d) != golden (%d/%d/%d)",
				p, len(got.IDs), len(got.Classes), len(got.Datas), g.ids, g.classes, g.datas)
		}
		// And the baseline's own listed sets must equal what we extract
		// (double-sided calibration: their file, our lens, same result).
		if _, extra := missingExtra(got.IDs, bl.IDs); len(extra) > 0 || len(missingOnly(bl.IDs, got.IDs)) > 0 {
			t.Errorf("%s: baseline-id list vs extracted-id set differ", p)
		}
	}
}

func missingOnly(want, have []string) []string {
	m, _ := missingExtra(want, have)
	return m
}

func baselineFile(page string) string {
	switch page {
	case "/admin/login":
		return "login.structure.md"
	case "/admin/accounts":
		return "accounts.structure.md"
	case "/admin/monitoring":
		return "monitoring.structure.md"
	case "/admin/settings":
		return "settings.structure.md"
	}
	return strings.TrimPrefix(page, "/admin/") + ".structure.md"
}

func printDiff(rb *strings.Builder, d Diff) {
	status := "✓ CLEAN"
	if !d.Clean() {
		status = "✗ DRIFT"
	}
	fmt.Fprintf(rb, "\n=== %s: %s ===\n", d.Page, status)
	emit := func(label string, xs ...[]string) {
		for _, x := range xs {
			if len(x) > 0 {
				fmt.Fprintf(rb, "  %s (%d): %s\n", label, len(x), strings.Join(x, " | "))
			}
		}
	}
	emit("missing id", d.MissingIDs)
	emit("EXTRA id (not in baseline)", d.ExtraIDs)
	emit("missing class", d.MissingClasses)
	emit("EXTRA class", d.ExtraClasses)
	emit("missing data-*", d.MissingDatas)
	emit("EXTRA data-*", d.ExtraDatas)
	emit("missing resource", d.MissingResources)
	emit("EXTRA resource", d.ExtraResources)
	emit("missing visible text", d.MissingTexts)
	emit("count mismatch", d.CountMismatch)
}
