// Package device manages remote SSH compute targets, their forwarded
// `atlas -serve` daemons, loopback tunnels, SSE event bridging, and
// cross-device project/session routing without third-party dependencies.
package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"pi-go/internal/httpapi"
	"pi-go/internal/project"
)

// FileName is the persisted device inventory file inside Atlas home.
const FileName = "devices.json"

var idSlugUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// Device describes one configured remote SSH host.
type Device struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user,omitempty"`
	IdentityFile  string `json:"identityFile,omitempty"`
	RemoteBin     string `json:"remoteBin,omitempty"`
	LastConnected string `json:"lastConnected,omitempty"`
}

type storeFile struct {
	Devices []Device `json:"devices"`
}

// Store persists configured remote SSH devices in JSON on disk.
type Store struct {
	mu   sync.Mutex
	path string
}

// OpenStore returns a device Store backed by path.
func OpenStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the backing JSON file path.
func (s *Store) Path() string { return s.path }

// List returns all saved remote devices in stored order.
func (s *Store) List() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	out := make([]Device, len(list))
	copy(out, list)
	return out
}

// Get fetches one configured device by ID.
func (s *Store) Get(id string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, dev := range s.load() {
		if dev.ID == id {
			return dev, true
		}
	}
	return Device{}, false
}

// Save creates (when id == "") or updates an existing device entry.
func (s *Store) Save(id string, input httpapi.DeviceInput) (Device, error) {
	clean, err := sanitizeInput(input)
	if err != nil {
		return Device{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()

	if id == "" {
		newID := uniqueID(slugify(clean.Name, clean.Host), list)
		dev := Device{
			ID:           newID,
			Name:         clean.Name,
			Host:         clean.Host,
			Port:         clean.Port,
			User:         clean.User,
			IdentityFile: clean.IdentityFile,
			RemoteBin:    clean.RemoteBin,
		}
		list = append(list, dev)
		if err := s.save(list); err != nil {
			return Device{}, err
		}
		return dev, nil
	}

	for i, existing := range list {
		if existing.ID == id {
			updated := Device{
				ID:            existing.ID,
				Name:          clean.Name,
				Host:          clean.Host,
				Port:          clean.Port,
				User:          clean.User,
				IdentityFile:  clean.IdentityFile,
				RemoteBin:     clean.RemoteBin,
				LastConnected: existing.LastConnected,
			}
			list[i] = updated
			if err := s.save(list); err != nil {
				return Device{}, err
			}
			return updated, nil
		}
	}
	return Device{}, fmt.Errorf("device: unknown device %q", id)
}

// Delete removes one configured device from the store.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	filtered := make([]Device, 0, len(list))
	found := false
	for _, dev := range list {
		if dev.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, dev)
	}
	if !found {
		return fmt.Errorf("device: unknown device %q", id)
	}
	return s.save(filtered)
}

// TouchConnected records a UTC connection timestamp for id.
func (s *Store) TouchConnected(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.load()
	for i := range list {
		if list[i].ID == id {
			list[i].LastConnected = time.Now().UTC().Format(time.RFC3339)
			return s.save(list)
		}
	}
	return nil
}

func sanitizeInput(input httpapi.DeviceInput) (httpapi.DeviceInput, error) {
	host := strings.TrimSpace(input.Host)
	if host == "" {
		return httpapi.DeviceInput{}, errors.New("device: host cannot be empty")
	}
	if err := validateNoShellMetachars("host", host, true); err != nil {
		return httpapi.DeviceInput{}, err
	}
	if input.Port < 0 || input.Port > 65535 {
		return httpapi.DeviceInput{}, fmt.Errorf("device: invalid port %d", input.Port)
	}
	user := strings.TrimSpace(input.User)
	if user != "" {
		if err := validateNoShellMetachars("user", user, true); err != nil {
			return httpapi.DeviceInput{}, err
		}
	}
	ident := strings.TrimSpace(input.IdentityFile)
	if strings.ContainsAny(ident, "\r\n\x00") {
		return httpapi.DeviceInput{}, errors.New("device: invalid identityFile")
	}
	remoteBin := strings.TrimSpace(input.RemoteBin)
	if strings.ContainsAny(remoteBin, "\r\n\x00") {
		return httpapi.DeviceInput{}, errors.New("device: invalid remoteBin")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = host
	}
	return httpapi.DeviceInput{
		Name:         name,
		Host:         host,
		Port:         input.Port,
		User:         user,
		IdentityFile: ident,
		RemoteBin:    remoteBin,
	}, nil
}

func validateNoShellMetachars(field, val string, rejectLeadingDash bool) error {
	if rejectLeadingDash && strings.HasPrefix(val, "-") {
		return fmt.Errorf("device: %s cannot start with '-'", field)
	}
	if strings.ContainsAny(val, " \t\r\n\x00") {
		return fmt.Errorf("device: %s cannot contain whitespace", field)
	}
	return nil
}

func slugify(name, host string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" {
		base = strings.ToLower(strings.TrimSpace(host))
	}
	slug := idSlugUnsafe.ReplaceAllString(base, "-")
	slug = strings.Trim(slug, "-_")
	if slug == "" || project.IsLocalDevice(slug) || slug == "all" {
		slug = "remote"
	}
	return slug
}

func uniqueID(base string, existing []Device) string {
	taken := make(map[string]bool, len(existing))
	for _, dev := range existing {
		taken[dev.ID] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

func (s *Store) load() []Device {
	if s.path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var payload storeFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload.Devices
}

func (s *Store) save(list []Device) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(storeFile{Devices: list}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := fmt.Sprintf("%s.tmp.%d", s.path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadSSHConfigHosts scans ~/.ssh/config for non-wildcard Host aliases to offer
// quick selection when adding a remote SSH device.
func ReadSSHConfigHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return parseSSHConfigFile(filepath.Join(home, ".ssh", "config"))
}

func parseSSHConfigFile(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var hosts []string
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if !strings.EqualFold(fields[0], "Host") {
			continue
		}
		for _, pattern := range fields[1:] {
			if pattern == "*" || strings.ContainsAny(pattern, "*?!") || strings.HasPrefix(pattern, "-") {
				continue
			}
			if seen[pattern] {
				continue
			}
			seen[pattern] = true
			hosts = append(hosts, pattern)
		}
	}
	return hosts
}
