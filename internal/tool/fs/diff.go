package fs

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// This file carries the text machinery behind the edit tool: line-ending
// handling, the fuzzy matcher that rescues an oldText whose invisible details
// drifted, and two diff renderings (a display diff and a unified patch).
//
// Everything is in process because pi's equivalents come from the npm "diff"
// package, which a static Go binary cannot depend on.

// detectLineEnding reports the dominant line ending so a rewritten file keeps
// the bytes it arrived with. A CRLF file that comes back as LF shows up as a
// whole-file change in git.
func detectLineEnding(content string) string {
	crlfIndex := strings.Index(content, "\r\n")
	lfIndex := strings.Index(content, "\n")
	if lfIndex == -1 || crlfIndex == -1 {
		return "\n"
	}
	if crlfIndex < lfIndex {
		return "\r\n"
	}
	return "\n"
}

// normalizeToLF converts every line ending to "\n" for matching.
func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// restoreLineEndings puts the original ending back after matching.
func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// normalizeForFuzzyMatch strips the differences a model cannot see: trailing
// whitespace, smart quotes, unicode dashes, and exotic spaces.
//
// pi additionally applies NFKC. Go's standard library has no normalization, so
// that step is skipped; the substitutions below cover what actually differs in
// practice. The omission only widens the set of edits that fail to match.
func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	text = strings.Join(lines, "\n")
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u2018', '\u2019', '\u201a', '\u201b':
			return '\''
		case '\u201c', '\u201d', '\u201e', '\u201f':
			return '"'
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			return '-'
		}
		if isUnicodeSpace(r) {
			return ' '
		}
		return r
	}, text)
}

// splitLinesWithEndings splits a string into lines that keep their "\n". The
// concatenation of the result is the input.
func splitLinesWithEndings(content string) []string {
	if content == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

type lineSpan struct {
	start int
	end   int
}

func getLineSpans(content string) []lineSpan {
	lines := splitLinesWithEndings(content)
	spans := make([]lineSpan, len(lines))
	offset := 0
	for i, line := range lines {
		spans[i] = lineSpan{start: offset, end: offset + len(line)}
		offset += len(line)
	}
	return spans
}

// replacement is one matched edit expressed as a byte range in the base content.
type replacement struct {
	matchIndex  int
	matchLength int
	newText     string
}

// applyReplacements rewrites content from the end so earlier offsets stay valid.
func applyReplacements(content string, replacements []replacement, offset int) string {
	result := content
	for i := len(replacements) - 1; i >= 0; i-- {
		edit := replacements[i]
		index := edit.matchIndex - offset
		result = result[:index] + edit.newText + result[index+edit.matchLength:]
	}
	return result
}

// getReplacementLineRange widens a replacement to whole lines.
func getReplacementLineRange(lines []lineSpan, edit replacement) (int, int, error) {
	start := edit.matchIndex
	end := edit.matchIndex + edit.matchLength

	startLine := -1
	for i, line := range lines {
		if start >= line.start && start < line.end {
			startLine = i
			break
		}
	}
	if startLine == -1 {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	endLine := startLine
	for endLine < len(lines) && lines[endLine].end < end {
		endLine++
	}
	if endLine >= len(lines) {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	return startLine, endLine + 1, nil
}

// applyReplacementsPreservingUnchangedLines applies replacements matched in the
// fuzzy-normalized view while copying unchanged lines from the original.
//
// Without this, one fuzzy match would rewrite the whole file in normalized form
// and strip trailing whitespace from lines the model never touched.
func applyReplacementsPreservingUnchangedLines(originalContent, baseContent string, replacements []replacement) (string, error) {
	originalLines := splitLinesWithEndings(originalContent)
	baseLines := getLineSpans(baseContent)
	if len(originalLines) != len(baseLines) {
		return "", fmt.Errorf("cannot preserve unchanged lines because the base content has a different line count")
	}

	type group struct {
		startLine    int
		endLine      int
		replacements []replacement
	}
	sorted := make([]replacement, len(replacements))
	copy(sorted, replacements)
	sortReplacementsByIndex(sorted)

	var groups []group
	for _, edit := range sorted {
		startLine, endLine, err := getReplacementLineRange(baseLines, edit)
		if err != nil {
			return "", err
		}
		if len(groups) > 0 {
			current := &groups[len(groups)-1]
			if startLine < current.endLine {
				current.endLine = max(current.endLine, endLine)
				current.replacements = append(current.replacements, edit)
				continue
			}
		}
		groups = append(groups, group{startLine: startLine, endLine: endLine, replacements: []replacement{edit}})
	}

	var out strings.Builder
	originalLineIndex := 0
	for _, g := range groups {
		for i := originalLineIndex; i < g.startLine; i++ {
			out.WriteString(originalLines[i])
		}
		groupStart := baseLines[g.startLine].start
		groupEnd := baseLines[g.endLine-1].end
		out.WriteString(applyReplacements(baseContent[groupStart:groupEnd], g.replacements, groupStart))
		originalLineIndex = g.endLine
	}
	for i := originalLineIndex; i < len(originalLines); i++ {
		out.WriteString(originalLines[i])
	}
	return out.String(), nil
}

func sortReplacementsByIndex(replacements []replacement) {
	for i := 1; i < len(replacements); i++ {
		for j := i; j > 0 && replacements[j].matchIndex < replacements[j-1].matchIndex; j-- {
			replacements[j], replacements[j-1] = replacements[j-1], replacements[j]
		}
	}
}

// diffPart is one run of identical, removed, or added lines.
type diffPart struct {
	lines   []string
	added   bool
	removed bool
}

// diffLineLimit bounds the Myers search. Beyond it a change is rendered as a
// whole-block replacement, which is still a correct diff, just coarser.
const diffLineLimit = 1000

// diffLines computes the line diff between two strings.
func diffLines(oldContent, newContent string) []diffPart {
	oldLines := splitDiffLines(oldContent)
	newLines := splitDiffLines(newContent)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	middleOld := oldLines[prefix : len(oldLines)-suffix]
	middleNew := newLines[prefix : len(newLines)-suffix]

	var parts []diffPart
	appendPart := func(lines []string, added, removed bool) {
		if len(lines) == 0 {
			return
		}
		parts = append(parts, diffPart{lines: lines, added: added, removed: removed})
	}
	appendPart(oldLines[:prefix], false, false)

	switch {
	case len(middleOld) == 0:
		appendPart(middleNew, true, false)
	case len(middleNew) == 0:
		appendPart(middleOld, false, true)
	case len(middleOld)+len(middleNew) > diffLineLimit:
		appendPart(middleOld, false, true)
		appendPart(middleNew, true, false)
	default:
		script := diffSequences(middleOld, middleNew)
		var current []string
		var kind opKind
		flush := func() {
			if len(current) == 0 {
				return
			}
			switch kind {
			case opEqual:
				appendPart(current, false, false)
			case opDelete:
				appendPart(current, false, true)
			case opInsert:
				appendPart(current, true, false)
			}
			current = nil
		}
		for _, op := range script {
			if op.kind != kind {
				flush()
				kind = op.kind
			}
			current = append(current, op.line)
		}
		flush()
	}

	appendPart(oldLines[len(oldLines)-suffix:], false, false)
	return parts
}

// splitDiffLines splits for diffing and drops the empty element a trailing
// newline produces, so line counts match what a reader sees.
func splitDiffLines(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type editOp struct {
	kind opKind
	line string
}

// diffSequences runs Myers' greedy algorithm with the trace kept so the script
// can be backtracked. Callers bound the input size.
func diffSequences(a, b []string) []editOp {
	n, m := len(a), len(b)
	maxD := n + m
	offset := maxD
	v := make([]int, 2*maxD+1)
	trace := make([][]int, 0, maxD+1)

	for d := 0; d <= maxD; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return backtrackEdits(trace, a, b, offset)
			}
		}
	}
	return nil
}

func backtrackEdits(trace [][]int, a, b []string, offset int) []editOp {
	var ops []editOp
	x, y := len(a), len(b)

	for d := len(trace) - 1; d > 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, editOp{kind: opEqual, line: a[x]})
		}
		if d > 0 {
			if x == prevX {
				y--
				ops = append(ops, editOp{kind: opInsert, line: b[y]})
			} else {
				x--
				ops = append(ops, editOp{kind: opDelete, line: a[x]})
			}
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, editOp{kind: opEqual, line: a[x]})
	}

	// Collected backwards; restore reading order.
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// generateDiffString renders a display diff with line numbers and a few lines of
// context, plus the first changed line in the new file for editor navigation.
func generateDiffString(oldContent, newContent string, contextLines int) (string, int) {
	parts := diffLines(oldContent, newContent)
	var output []string

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	maxLineNum := max(len(oldLines), len(newLines))
	lineNumWidth := len(strconv.Itoa(maxLineNum))

	oldLineNum, newLineNum := 1, 1
	lastWasChange := false
	firstChangedLine := 0

	pad := func(n int) string {
		text := strconv.Itoa(n)
		if len(text) >= lineNumWidth {
			return text
		}
		return strings.Repeat(" ", lineNumWidth-len(text)) + text
	}
	gap := " " + strings.Repeat(" ", lineNumWidth) + " ..."

	for i, part := range parts {
		raw := part.lines
		if part.added || part.removed {
			if firstChangedLine == 0 {
				firstChangedLine = newLineNum
			}
			for _, line := range raw {
				if part.added {
					output = append(output, "+"+pad(newLineNum)+" "+line)
					newLineNum++
				} else {
					output = append(output, "-"+pad(oldLineNum)+" "+line)
					oldLineNum++
				}
			}
			lastWasChange = true
			continue
		}

		nextIsChange := i < len(parts)-1 && (parts[i+1].added || parts[i+1].removed)
		switch {
		case lastWasChange && nextIsChange:
			if len(raw) <= contextLines*2 {
				for _, line := range raw {
					output = append(output, " "+pad(oldLineNum)+" "+line)
					oldLineNum++
					newLineNum++
				}
			} else {
				for _, line := range raw[:contextLines] {
					output = append(output, " "+pad(oldLineNum)+" "+line)
					oldLineNum++
					newLineNum++
				}
				output = append(output, gap)
				skipped := len(raw) - contextLines*2
				oldLineNum += skipped
				newLineNum += skipped
				for _, line := range raw[len(raw)-contextLines:] {
					output = append(output, " "+pad(oldLineNum)+" "+line)
					oldLineNum++
					newLineNum++
				}
			}
		case lastWasChange:
			shown := raw
			if len(shown) > contextLines {
				shown = shown[:contextLines]
			}
			for _, line := range shown {
				output = append(output, " "+pad(oldLineNum)+" "+line)
				oldLineNum++
				newLineNum++
			}
			if skipped := len(raw) - len(shown); skipped > 0 {
				output = append(output, gap)
				oldLineNum += skipped
				newLineNum += skipped
			}
		case nextIsChange:
			skipped := max(0, len(raw)-contextLines)
			if skipped > 0 {
				output = append(output, gap)
				oldLineNum += skipped
				newLineNum += skipped
			}
			for _, line := range raw[skipped:] {
				output = append(output, " "+pad(oldLineNum)+" "+line)
				oldLineNum++
				newLineNum++
			}
		default:
			oldLineNum += len(raw)
			newLineNum += len(raw)
		}
		lastWasChange = false
	}

	return strings.Join(output, "\n"), firstChangedLine
}

// generateUnifiedPatch renders a standard unified patch. Hunks carry
// contextLines of surrounding unchanged lines, merged when they would overlap.
func generateUnifiedPatch(path, oldContent, newContent string, contextLines int) string {
	parts := diffLines(oldContent, newContent)

	type hunk struct {
		oldStart, oldCount int
		newStart, newCount int
		lines              []string
	}

	var hunks []hunk
	oldLine, newLine := 1, 1
	i := 0
	for i < len(parts) {
		if !parts[i].added && !parts[i].removed {
			oldLine += len(parts[i].lines)
			newLine += len(parts[i].lines)
			i++
			continue
		}

		// Walk back over unchanged lines to pick up leading context.
		leading := []string{}
		if i > 0 && !parts[i-1].added && !parts[i-1].removed {
			previous := parts[i-1].lines
			take := min(contextLines, len(previous))
			leading = previous[len(previous)-take:]
			oldLine -= take
			newLine -= take
		}

		current := hunk{oldStart: oldLine, newStart: newLine}
		for _, line := range leading {
			current.lines = append(current.lines, " "+line)
			current.oldCount++
			current.newCount++
			oldLine++
			newLine++
		}

		for i < len(parts) && (parts[i].added || parts[i].removed) {
			for _, line := range parts[i].lines {
				if parts[i].added {
					current.lines = append(current.lines, "+"+line)
					current.newCount++
					newLine++
				} else {
					current.lines = append(current.lines, "-"+line)
					current.oldCount++
					oldLine++
				}
			}
			i++
		}

		if i < len(parts) && !parts[i].added && !parts[i].removed {
			next := parts[i].lines
			take := min(contextLines, len(next))
			for _, line := range next[:take] {
				current.lines = append(current.lines, " "+line)
				current.oldCount++
				current.newCount++
				oldLine++
				newLine++
			}
		}

		hunks = append(hunks, current)
	}

	var out strings.Builder
	out.WriteString("--- " + path + "\n")
	out.WriteString("+++ " + path + "\n")
	for _, h := range hunks {
		out.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount))
		for _, line := range h.lines {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	return out.String()
}
