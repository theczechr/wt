package discover

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theczechr/wt/internal/model"
)

// chunkSize is how much of a transcript's tail is read per pass.
const chunkSize = 64 * 1024

// maxTail bounds total tail reading, so a pathological file cannot be walked
// in full.
const maxTail = 256 * 1024

type record struct {
	Type       string `json:"type"`
	AITitle    string `json:"aiTitle"`
	LastPrompt string `json:"lastPrompt"`
	SessionID  string `json:"sessionId"`
	PRNumber   int    `json:"prNumber"`
	Cwd        string `json:"cwd"`
	GitBranch  string `json:"gitBranch"`
}

// ReadSessionTail reads a transcript backwards, collecting the last ai-title,
// last-prompt, pr-link, and cwd/branch it finds. A malformed or unreadable
// file yields a zero-valued Session and no error: collectors degrade, they do
// not fail.
func ReadSessionTail(path string) (model.Session, error) {
	s := model.Session{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}

	f, err := os.Open(path)
	if err != nil {
		return s, nil
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return s, nil
	}
	s.Mtime = st.ModTime()
	size := st.Size()

	var (
		tail    []byte
		scanned int64
	)
	for scanned < size && scanned < maxTail {
		step := int64(chunkSize)
		if remaining := size - scanned; remaining < step {
			step = remaining
		}
		// Clamp against the maxTail budget too, not just what's left in the
		// file. This is a no-op today because chunkSize evenly divides
		// maxTail, but without it, tuning either constant so it no longer
		// divides evenly would let a single step overshoot maxTail and read
		// further back than the documented cap.
		if budget := maxTail - scanned; budget < step {
			step = budget
		}
		offset := size - scanned - step
		buf := make([]byte, step)
		if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
			break
		}
		tail = append(buf, tail...)
		scanned += step

		// The tail buffer's first line is only a genuine, complete record
		// once the read has reached the start of the file (offset 0).
		// Before that, it's the truncated remainder of a line that
		// continues into the part of the file we haven't read yet.
		if complete(parseLines(tail, &s, offset == 0)) {
			break
		}
	}
	s.BytesScanned = int(scanned)
	parseLines(tail, &s, scanned >= size)
	return s, nil
}

type found struct{ title, prompt, pr, cwd bool }

func complete(f found) bool { return f.title && f.prompt && f.pr && f.cwd }

// parseLines walks the buffer newest-line-first, filling only fields that are
// still empty, so the LAST occurrence in the file wins. Unless atStart is
// true, the first line of the buffer may be a partial record and is skipped.
func parseLines(buf []byte, s *model.Session, atStart bool) found {
	lines := strings.Split(string(buf), "\n")
	if !atStart && len(lines) > 0 {
		lines = lines[1:]
	}
	var f found
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var r record
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		switch r.Type {
		case "ai-title":
			if s.Title == "" {
				s.Title = r.AITitle
			}
		case "last-prompt":
			if s.LastPrompt == "" {
				s.LastPrompt = r.LastPrompt
			}
		case "pr-link":
			if s.PRNumber == 0 {
				s.PRNumber = r.PRNumber
			}
		case "user":
			if s.Cwd == "" {
				s.Cwd = r.Cwd
			}
			if s.Branch == "" {
				s.Branch = r.GitBranch
			}
		}
		if r.SessionID != "" && s.ID == "" {
			s.ID = r.SessionID
		}
	}
	f.title = s.Title != ""
	f.prompt = s.LastPrompt != ""
	f.pr = s.PRNumber != 0
	f.cwd = s.Cwd != ""
	return f
}

// SessionsFor returns every session recorded for a worktree, newest first.
// cache may be nil, in which case every transcript is read live, exactly as
// before this existed; when non-nil, a transcript whose (mtime, size) still
// matches a cached entry skips the tail read entirely.
func SessionsFor(worktreePath string, cache *SessionCache) []model.Session {
	dir := filepath.Join(model.ClaudeProjectsDir(), model.ProjectDirName(worktreePath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []model.Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())

		var s model.Session
		info, statErr := e.Info()
		if statErr == nil {
			if cached, ok := cache.Get(path, info.ModTime(), info.Size()); ok {
				s = cached
			} else {
				s, _ = ReadSessionTail(path)
				cache.Put(path, s, info.Size())
			}
		} else {
			// Stat failed (e.g. the file vanished between ReadDir and here);
			// fall back to a live read rather than caching against an
			// identity we couldn't establish.
			s, _ = ReadSessionTail(path)
		}

		if s.Cwd == "" {
			s.Cwd = worktreePath
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mtime.After(out[j].Mtime) })
	return out
}
