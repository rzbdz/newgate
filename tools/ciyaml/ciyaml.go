// Package ciyaml checks the **structure** of .github/workflows/*.yml -- it does not do a full
// YAML parse, it only recognizes "the hard errors you can see at a glance from the
// indentation".
//
// Why it deserves its own layer (the lesson of 2026-09-20): editing the workflows while
// flattening go/ deleted a step's `run:` line together with `working-directory`, leaving this:
//
//   - name: opencode e2e
//
// with neither `run` nor `uses`. That made the workflow **invalid**, and GitHub rejected the
// whole run in 0 seconds -- not a single job started. So the CI of the next six commits was all
// red, looking like "the tests are broken" when in fact **the tests never ran**. A failure like
// that has to be shouted by someone, and that someone cannot be GitHub: by the time it speaks,
// the thing is already pushed.
//
// So the same check is moved **locally** -- the kernel's `go test ./...` (app/ci_test.go) and
// the distribution's dist-test (testing/ci_test.go) both run it. The criteria are deliberately
// conservative: rather miss something than raise a false alarm, because a ratchet that raises
// false alarms always ends up being worked around.
//
// A full parse would need a YAML library, and this repository **forbids third-party
// dependencies** -- the same trade-off already produced the hand-written East Asian Width in
// lib/style and the hand-written scanner in tools/i18n.
package ciyaml

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one structural problem. There is no Level: everything reported here is a hard
// error that makes GitHub reject the whole workflow -- either everything is red or nothing is,
// and two levels would only make people think "warn can be ignored for now".
type Finding struct {
	File string // path relative to root
	Line int    // 1-based
	Msg  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Msg)
}

// Check scans every *.yml/*.yaml under root/.github/workflows.
//
// When the directory does not exist, or there is not a single workflow, it returns an error
// rather than an empty result: that means **the check itself has degenerated**, not "the
// repository is clean". An empty result must only ever mean "checked, no problems".
func Check(root string) ([]Finding, error) {
	dir := filepath.Join(root, ".github", "workflows")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", dir, err)
	}
	var files []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") {
			files = append(files, filepath.Join(dir, n))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s has not a single workflow -- the check has degenerated", dir)
	}
	sort.Strings(files)

	var out []Finding
	for _, f := range files {
		fs, err := checkFile(root, f)
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}
	return out, nil
}

// yline is one line of effective content (blank lines and whole-line comments already filtered
// out), with the original indent column.
type yline struct {
	no     int
	indent int
	text   string // already TrimSpace'd
}

func (l yline) isItem() bool { return l.text == "-" || strings.HasPrefix(l.text, "- ") }

func checkFile(root, path string) ([]Finding, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	rel, rerr := filepath.Rel(root, path)
	if rerr != nil {
		rel = path
	}

	var (
		out   []Finding
		lines []yline
	)
	for i, s := range strings.Split(string(raw), "\n") {
		no := i + 1
		indent := 0
		for indent < len(s) && s[indent] == ' ' {
			indent++
		}
		// YAML indentation must not use tabs. If such a line slips in, the error message points at a
		// completely unrelated column and is painful to track down -- so this names it directly.
		if indent < len(s) && s[indent] == '\t' {
			out = append(out, Finding{rel, no, "a tab appeared in the indentation -- YAML does not accept tab indentation"})
		}
		text := strings.TrimSpace(s)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		lines = append(lines, yline{no: no, indent: indent, text: text})
	}

	// Empty value: nothing after `run:` (the next line has no deeper-indented subkey). This is the
	// other half of that original bug -- three jobs' `defaults: run:` were deleted down to an
	// empty shell.
	for i, l := range lines {
		key, ok := emptyableKey(l.text)
		if !ok {
			continue
		}
		if i+1 < len(lines) && lines[i+1].indent > l.indent {
			continue // it has child keys, so it is not an empty value
		}
		out = append(out, Finding{rel, l.no,
			fmt.Sprintf("\"%s:\" is an empty value -- give it a value or delete the whole block", key)})
	}

	// Under jobs:, look at each job one by one. No jobs: found means the whole file was written
	// for nothing.
	jobsAt := -1
	for i, l := range lines {
		if l.indent == 0 && l.text == "jobs:" {
			jobsAt = i
			break
		}
	}
	if jobsAt < 0 {
		return append(out, Finding{rel, 1, "no top-level jobs: -- this workflow will not do anything"}), nil
	}
	rest := lines[jobsAt+1:]
	if len(rest) == 0 {
		return append(out, Finding{rel, lines[jobsAt].no, "jobs: is empty"}), nil
	}

	// Job names = the `name:` lines right after jobs:, at the same indent as the first key.
	jobIndent := rest[0].indent
	for i := 0; i < len(rest); {
		if rest[i].indent != jobIndent || !strings.HasSuffix(rest[i].text, ":") {
			i++
			continue
		}
		name := strings.TrimSuffix(rest[i].text, ":")
		end := i + 1
		for end < len(rest) && rest[end].indent > jobIndent {
			end++
		}
		out = append(out, checkJob(rel, rest[i:end], name)...)
		i = end
	}
	return out, nil
}

// checkJob looks at one job: it must have runs-on (otherwise GitHub rejects it), and every step
// under steps must have run or uses.
func checkJob(rel string, body []yline, name string) []Finding {
	var out []Finding
	hasRunsOn := false
	stepsAt := -1
	for i, l := range body {
		if strings.HasPrefix(l.text, "runs-on:") && l.indent > body[0].indent {
			hasRunsOn = true
		}
		if l.text == "steps:" {
			stepsAt = i
		}
	}
	if !hasRunsOn {
		out = append(out, Finding{rel, body[0].no,
			fmt.Sprintf("job \"%s\" has no runs-on -- GitHub will reject the whole workflow", name)})
	}
	if stepsAt < 0 {
		return append(out, Finding{rel, body[0].no,
			fmt.Sprintf("job \"%s\" has no steps", name)})
	}

	items := body[stepsAt+1:]
	if len(items) == 0 {
		return append(out, Finding{rel, body[stepsAt].no,
			fmt.Sprintf("the steps: of job \"%s\" is empty", name)})
	}
	itemIndent := items[0].indent
	for i := 0; i < len(items); {
		if items[i].indent != itemIndent || !items[i].isItem() {
			i++
			continue
		}
		end := i + 1
		for end < len(items) && items[end].indent > itemIndent {
			end++
		}
		if f, bad := checkStep(rel, items[i:end], itemIndent); bad {
			out = append(out, f)
		}
		i = end
	}
	return out
}

// checkStep: a step must have run or uses. Keys count in two places: `- uses: x` written on the
// dash line itself, or written below it at the same level as `run:` / `uses:` . Deeper keys
// (the children of `with:` ) do not count -- that layer is parameters, not the kind of this
// step.
func checkStep(rel string, item []yline, itemIndent int) (Finding, bool) {
	hasBody, stepName := false, ""
	dash := strings.TrimSpace(strings.TrimPrefix(item[0].text, "-"))
	if k, v, ok := splitKey(dash); ok {
		switch k {
		case "run", "uses":
			hasBody = true
		case "name":
			stepName = v
		}
	}
	for _, l := range item[1:] {
		if l.indent != itemIndent+2 {
			continue
		}
		k, v, ok := splitKey(l.text)
		if !ok {
			continue
		}
		switch k {
		case "run", "uses":
			hasBody = true
		case "name":
			stepName = v
		}
	}
	if hasBody {
		return Finding{}, false
	}
	what := "This step"
	if stepName != "" {
		what = fmt.Sprintf("step \"%s\"", stepName)
	}
	return Finding{rel, item[0].no,
		what + " has neither run nor uses -- GitHub will reject the whole workflow and not a single job will start"}, true
}

// emptyableKey returns the key names that "must carry a value". An empty value (the next line
// has no deeper subkey) is a hard error: `run:` left empty means the step does nothing.
func emptyableKey(text string) (string, bool) {
	for _, k := range []string{"run", "shell", "uses", "runs-on", "steps"} {
		if text == k+":" {
			return k, true
		}
	}
	return "", false
}

// splitKey splits `name: value` into key and value; returns ok=false when it is not "key:
// value" shaped. The value may contain a colon (`run: echo a:b`), so only the first one is
// split on.
func splitKey(s string) (key, val string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:i])
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(s[i+1:]), true
}
