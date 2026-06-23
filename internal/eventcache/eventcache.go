package eventcache

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const envCacheDir = "AEXP_EVENT_CACHE_DIR"

type Line struct {
	LineNo  int
	Content string
}

func Path(runID string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	name, err := filename(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func LastSnapshotPath(runID string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	name, err := filename(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.TrimSuffix(name, ".jsonl")+"_last_snapshot.json"), nil
}

func Dir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(envCacheDir)); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aexp", "event_cache"), nil
}

func Read(runID string, lastN int) ([]Line, string, error) {
	path, err := Path(runID)
	if err != nil {
		return nil, "", err
	}
	lines, err := readContents(path)
	if err != nil {
		return nil, path, err
	}
	firstLineNo := 1
	if lastN > 0 && len(lines) > lastN {
		firstLineNo = len(lines) - lastN + 1
		lines = lines[len(lines)-lastN:]
	}
	out := make([]Line, 0, len(lines))
	for i, content := range lines {
		out = append(out, Line{LineNo: firstLineNo + i, Content: content})
	}
	return out, path, nil
}

func Write(runID string, lines []Line) (string, error) {
	path, err := Path(runID)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return path, err
	}
	existing, err := readContents(path)
	if err != nil && !os.IsNotExist(err) {
		return path, err
	}

	incoming := make([]string, 0, len(lines))
	first := lines[0].LineNo
	if first <= 0 {
		first = 1
	}
	for _, line := range lines {
		content := strings.TrimRight(line.Content, "\r\n")
		incoming = append(incoming, content)
	}

	var merged []string
	if len(existing) == 0 || first > len(existing)+1 {
		merged = incoming
	} else {
		start := first - 1
		if start < 0 {
			start = 0
		}
		merged = append([]string{}, existing[:start]...)
		merged = append(merged, incoming...)
		if next := start + len(incoming); next < len(existing) {
			merged = append(merged, existing[next:]...)
		}
	}
	return path, writeAtomic(path, merged)
}

func filename(runID string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", fmt.Errorf("run id is required")
	}
	var b strings.Builder
	for _, r := range runID {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + ".jsonl", nil
}

func readContents(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func writeAtomic(path string, lines []string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	writer := bufio.NewWriter(tmp)
	for _, line := range lines {
		if _, err := writer.WriteString(line); err != nil {
			tmp.Close()
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
