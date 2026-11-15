package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AgentDir returns the pi agent directory: $PI_CODING_AGENT_DIR, else
// ~/.pi/agent. Session files live under its sessions/ subdirectory.
func AgentDir() string {
	if env := os.Getenv("PI_CODING_AGENT_DIR"); env != "" {
		if resolved, err := absolutePath(env); err == nil {
			return resolved
		}
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// DefaultDir returns the session directory for a working directory. The path
// encodes cwd the way pi does, so both tools read the same files.
func DefaultDir(cwd, agentDir string) string {
	return defaultDirPath(cwd, agentDir)
}

func defaultDirPath(cwd, agentDir string) string {
	root := agentDir
	if root == "" {
		root = AgentDir()
	}
	resolved, err := absolutePath(cwd)
	if err != nil {
		resolved = cwd
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(resolved, "/"), `\`)
	encoded := strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(trimmed)
	return filepath.Join(root, "sessions", "--"+encoded+"--")
}

// Info is the summary shown by a session picker. It comes from a streaming read
// that never holds a whole transcript in memory.
type Info struct {
	Path              string
	ID                string
	Cwd               string
	Name              string
	ParentSessionPath string
	Created           time.Time
	Modified          time.Time
	MessageCount      int
	FirstMessage      string
	AllMessagesText   string
}

// List reads every session in dir, newest activity first. Unreadable or invalid
// files are skipped: one corrupt file must not hide the rest. A non-empty cwd
// keeps only sessions whose header records that directory, which is what a
// shared session directory needs: two projects can encode to the same name.
//
// Archived sessions are left out. Their files are untouched, which is the point
// of archiving, but they belong to the list /recover draws rather than to the
// one a picker opens conversations from.
func List(dir, cwd string) ([]Info, error) { return listSessions(dir, cwd, false) }

// ListArchived is the other half of List: only the sessions the user archived.
func ListArchived(dir, cwd string) ([]Info, error) { return listSessions(dir, cwd, true) }

func listSessions(dir, cwd string, archived bool) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var infos []Info
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if Archived(path) != archived {
			continue
		}
		info, err := ReadInfo(path)
		if err != nil || info == nil {
			continue
		}
		// samePath, not ==: a header written with a trailing separator still
		// names the same directory.
		if cwd != "" && !samePath(info.Cwd, cwd) {
			continue
		}
		infos = append(infos, *info)
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Modified.After(infos[j].Modified)
	})
	return infos, nil
}

// ListAll reads every session under <agentDir>/sessions, one bucket per project.
// A directory that cannot be read is skipped: one broken bucket must not hide
// the rest. It does not guess which project is current — Info.Cwd is there for
// the caller to compare.
func ListAll(agentDir string) ([]Info, error) { return listBuckets(agentDir, false) }

// ListArchivedAll is the other half of ListAll: every archived session, one
// bucket per project. /recover --all is the command that draws it, and a bucket
// that cannot be read is skipped for the same reason.
func ListArchivedAll(agentDir string) ([]Info, error) { return listBuckets(agentDir, true) }

func listBuckets(agentDir string, archived bool) ([]Info, error) {
	root := filepath.Join(agentDir, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var infos []Info
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			// A bucket may be a symlink to a directory elsewhere; anything
			// that is not a directory is not a bucket.
			stat, statErr := os.Stat(dir)
			if statErr != nil || !stat.IsDir() {
				continue
			}
		}
		found, listErr := listSessions(dir, "", archived)
		if listErr != nil {
			continue
		}
		infos = append(infos, found...)
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Modified.After(infos[j].Modified)
	})
	return infos, nil
}

// ReadInfo summarizes one session file. It returns nil for a file that is not a
// session.
func ReadInfo(path string) (*Info, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	stats, err := handle.Stat()
	if err != nil {
		return nil, err
	}

	var (
		header       *Header
		name         string
		messageCount int
		firstMessage string
		allMessages  []string
		lastActivity int64
	)
	reader := bufio.NewReaderSize(handle, readBufferSize)
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			if header == nil {
				var probe struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				}
				if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
					return nil, nil
				}
				if probe.Type != "session" || probe.ID == "" {
					return nil, nil
				}
				decoded, decodeErr := decodeHeader([]byte(trimmed))
				if decodeErr != nil {
					return nil, nil
				}
				header = &decoded
			} else if entry, decodeErr := decodeEntry([]byte(trimmed)); decodeErr == nil {
				switch entry.Type {
				case TypeSessionInfo:
					name = strings.TrimSpace(entry.Name)
				case TypeMessage:
					messageCount++
					meta := messageMetaOf(entry)
					if meta.Role != string(roleUser) && meta.Role != string(roleAssistant) {
						continue
					}
					activity := meta.Timestamp
					if activity == 0 {
						activity = millis(entry.Timestamp)
					}
					if activity > lastActivity {
						lastActivity = activity
					}
					text := contentText(meta.Content)
					if text == "" {
						continue
					}
					allMessages = append(allMessages, text)
					if firstMessage == "" && meta.Role == string(roleUser) {
						firstMessage = text
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if header == nil {
		return nil, nil
	}

	created, err := time.Parse(time.RFC3339Nano, header.Timestamp)
	if err != nil {
		created = stats.ModTime()
	}
	// pi prefers the newest user/assistant timestamp, then the header time, then
	// the file mtime (via created above).
	modified := created
	if lastActivity > 0 {
		modified = time.UnixMilli(lastActivity)
	}
	if firstMessage == "" {
		firstMessage = "(no messages)"
	}
	return &Info{
		Path:              path,
		ID:                header.ID,
		Cwd:               header.Cwd,
		Name:              name,
		ParentSessionPath: header.ParentSession,
		Created:           created,
		Modified:          modified,
		MessageCount:      messageCount,
		FirstMessage:      firstMessage,
		AllMessagesText:   strings.Join(allMessages, " "),
	}, nil
}

// roleUser and roleAssistant exist so list.go does not import wire just for two
// constants.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// FindMostRecent returns the newest session file in dir. When cwd is non-empty
// only sessions recorded for that cwd match (samePath, so a trailing separator
// does not matter), which is what a shared session directory needs; a session
// that recorded no cwd never matches, because it belongs to no project.
//
// Sessions without a conversation entry are skipped: a header-only file is a
// placeholder a switch left behind, not the last conversation, and -continue
// must not open an empty one over a real session that is older.
func FindMostRecent(dir, cwd string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		candidates = append(candidates, candidate{path: path, modTime: info.ModTime()})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	for _, candidate := range candidates {
		info, err := ReadInfo(candidate.path)
		if err != nil || info == nil {
			continue
		}
		if info.MessageCount == 0 {
			continue
		}
		// An archived conversation is one the user put away. Continuing it
		// here would walk it straight back into the picker they hid it from.
		if Archived(candidate.path) {
			continue
		}
		// The same predicate as List. A session with no recorded cwd cannot
		// belong to any project, so a filtered call skips it: only -session
		// <path> opens one by hand.
		if cwd != "" && !samePath(info.Cwd, cwd) {
			continue
		}
		return candidate.path
	}
	return ""
}

// FindByID returns the path of the session with the exact id, or "".
func FindByID(dir, id, cwd string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		header, scanErr := ScanHeader(path)
		if scanErr != nil || header == nil || header.ID != id {
			continue
		}
		// The same predicate as List and FindMostRecent: a session with no
		// recorded cwd belongs to no project.
		if cwd != "" && !samePath(header.Cwd, cwd) {
			continue
		}
		return path
	}
	return ""
}

// samePath compares directories after cleaning both, so /x/y and /x/y/ match.
func samePath(a, b string) bool {
	left, errLeft := absolutePath(a)
	right, errRight := absolutePath(b)
	if errLeft != nil || errRight != nil {
		return a == b
	}
	return left == right
}

func messageMetaOf(entry Entry) messageMeta {
	var meta messageMeta
	_ = json.Unmarshal(entry.Message, &meta)
	return meta
}

// contentText flattens message content to text for search and previews.
func contentText(raw json.RawMessage) string {
	content, err := contentValue(raw)
	if err != nil {
		return ""
	}
	if content.IsPlain() {
		return content.PlainText()
	}
	var parts []string
	for _, block := range content.Items() {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, " ")
}
