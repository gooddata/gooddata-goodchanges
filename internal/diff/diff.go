package diff

import (
	"strconv"
	"strings"
)

type LineRange struct {
	Start int // 1-based
	End   int // 1-based, inclusive
}

type FileDiff struct {
	Path         string
	ChangedLines []LineRange
}

// ParseFiles parses a unified diff and returns changed files with their changed line ranges.
// The line ranges refer to the NEW file (post-change) line numbers.
func ParseFiles(diff string) []FileDiff {
	var result []FileDiff
	lines := strings.Split(diff, "\n")

	var current *FileDiff
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			path := strings.TrimPrefix(line, "+++ b/")
			result = append(result, FileDiff{Path: path})
			current = &result[len(result)-1]
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			r := parseHunkHeader(line)
			if r.Start > 0 {
				current.ChangedLines = append(current.ChangedLines, r)
			}
		}
	}
	return result
}

func parseHunkHeader(line string) LineRange {
	plusIdx := strings.Index(line, "+")
	if plusIdx < 0 {
		return LineRange{}
	}
	rest := line[plusIdx+1:]
	spaceIdx := strings.Index(rest, " ")
	if spaceIdx < 0 {
		return LineRange{}
	}
	rangeStr := rest[:spaceIdx]

	parts := strings.SplitN(rangeStr, ",", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return LineRange{}
	}
	count := 1
	if len(parts) == 2 {
		count, err = strconv.Atoi(parts[1])
		if err != nil {
			return LineRange{}
		}
	}
	if count == 0 {
		count = 1
	}
	return LineRange{Start: start, End: start + count - 1}
}
