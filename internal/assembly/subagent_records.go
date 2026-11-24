package assembly

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

func createSubagentRecord(path string, up wire.SubagentUpdate) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lease, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if locked, err := trySubagentLock(lease); err != nil || !locked {
		releaseSubagentLease(path, lease, true)
		if err == nil {
			err = errors.New("could not lock new record")
		}
		return nil, err
	}
	if err := writeSubagentRecord(path, up); err != nil {
		releaseSubagentLease(path, lease, true)
		return nil, err
	}
	return lease, nil
}

func releaseSubagentLease(path string, lease *os.File, remove bool) {
	if remove {
		_ = os.Remove(path + ".lock")
	}
	_ = lease.Close()
}

func writeSubagentRecord(path string, up wire.SubagentUpdate) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".subagent-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(up)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), path)
}

func readSubagentRecord(path string) (wire.SubagentUpdate, error) {
	file, err := os.Open(path)
	if err != nil {
		return wire.SubagentUpdate{}, err
	}
	defer file.Close()
	var up wire.SubagentUpdate
	if err := json.NewDecoder(io.LimitReader(file, 128<<10)).Decode(&up); err != nil {
		return up, fmt.Errorf("read subagent record %s: %w", filepath.Base(path), err)
	}
	return up, nil
}

func completedSubagentRecords(sessionID, sessionFile string) ([]wire.SubagentUpdate, error) {
	dir := session.SubagentDir(sessionFile)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var results []wire.SubagentUpdate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		up, err := readSubagentRecord(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // Turn cleanup may remove uncommitted records during the scan.
		}
		if err != nil {
			return nil, err
		}
		if up.SessionID != sessionID || up.SessionFile != sessionFile || up.ID+".json" != entry.Name() {
			continue
		}
		if up.State == subagentQueued || up.State == subagentRunning {
			up, err = recoverSubagentRecord(path)
			if err != nil {
				return nil, err
			}
		}
		if up.EntryID == "" {
			continue
		}
		switch up.State {
		case subagentSucceeded, subagentFailed, subagentCanceled, subagentLost:
			results = append(results, up)
		}
	}
	return results, nil
}

func recoverSubagentRecord(path string) (wire.SubagentUpdate, error) {
	lease, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		// A writer can publish the terminal record and unlink its lease between
		// our read of the queued or running snapshot and the open above.
		up, err := readSubagentRecord(path)
		if errors.Is(err, os.ErrNotExist) {
			return wire.SubagentUpdate{}, nil
		}
		return up, err
	}
	if err != nil {
		return wire.SubagentUpdate{}, err
	}
	defer lease.Close()
	locked, err := trySubagentLock(lease)
	if err != nil || !locked {
		return wire.SubagentUpdate{}, err
	}
	// Another writer may have finished while we were opening the lease. Only
	// the process holding the lock may replace an unfinished record.
	up, err := readSubagentRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return wire.SubagentUpdate{}, nil
	}
	if err != nil || (up.State != subagentQueued && up.State != subagentRunning) {
		return up, err
	}
	if up.EntryID == "" {
		// The previous process can die after the receipt is appended but before
		// Commit writes its entry ID into the task record.
		up.EntryID, err = findSubagentReceipt(up)
		if err != nil || up.EntryID == "" {
			return wire.SubagentUpdate{}, err
		}
	}
	up.State, up.Error = subagentLost, "the previous subagent runner exited before completing this task"
	if err := writeSubagentRecord(path, up); err != nil {
		return wire.SubagentUpdate{}, err
	}
	_ = os.Remove(path + ".lock")
	return up, nil
}

func findSubagentReceipt(up wire.SubagentUpdate) (string, error) {
	file, err := os.Open(up.SessionFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		var entry session.Entry
		if json.Unmarshal(line, &entry) != nil || entry.Type != session.TypeMessage {
			continue
		}
		var message struct {
			Role       wire.Role       `json:"role"`
			ToolName   string          `json:"toolName"`
			ToolCallID string          `json:"toolCallId"`
			Details    json.RawMessage `json:"details"`
		}
		if json.Unmarshal(entry.Message, &message) != nil || message.Role != wire.RoleToolResult || message.ToolName != wire.SubagentToolName || message.ToolCallID != up.ToolCallID {
			continue
		}
		var receipt wire.SubagentUpdate
		if json.Unmarshal(message.Details, &receipt) == nil && receipt.ID == up.ID && receipt.SessionID == up.SessionID && receipt.State == subagentQueued {
			return entry.ID, nil
		}
	}
}
