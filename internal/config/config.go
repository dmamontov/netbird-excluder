// Package config persists the exclusion list (domains and literal IPv4
// addresses) and the enabled/disabled state as a hand-editable YAML file, so
// it can be managed either by editing the file or via CLI commands, and
// picked up live by an already-running daemon.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	Path       = "/etc/netbird-excluder/config.yaml"
	LegacyPath = "/etc/netbird-excluder/config.json"
)

// template is the starting point for a config file that doesn't exist yet,
// so a freshly written file documents itself.
const template = `# netbird-excluder config. Edit by hand or via
# "netbird-excluder add|remove|up|down"; a running daemon picks up changes on
# its next pass. Check your edits with "netbird-excluder validate".

# false pauses enforcement and restores every overridden route.
enabled: true
exclude:
  # Re-resolved on every pass; every IPv4 address they currently resolve to
  # is routed via the LAN gateway instead of through NetBird.
  domains: []
  # Literal IPv4 addresses to route via the LAN gateway instead of NetBird.
  ips: []
`

type Config struct {
	Enabled bool
	Domains []string
	IPs     []string

	// doc is the parsed YAML document, kept so Save only touches the values
	// that changed and leaves the user's comments and layout alone.
	doc *yaml.Node
}

// fileFormat is the on-disk schema, used to reject unknown keys and wrong
// types. Values are validated separately, against the node tree, so errors
// can point at a line.
type fileFormat struct {
	Enabled *bool `yaml:"enabled"`
	Exclude struct {
		Domains []string `yaml:"domains"`
		IPs     []string `yaml:"ips"`
	} `yaml:"exclude"`
}

// Load reads the config file. If it doesn't exist yet but a config.json from
// an older version does, that is migrated (or, without permission to write,
// just read). With neither, a default (enabled, empty) config is returned.
func Load() (*Config, error) {
	return load(Path, LegacyPath)
}

// Save atomically writes c to the config file, preserving comments and
// layout of the file it was loaded from.
func Save(c *Config) error {
	return save(c, Path, LegacyPath)
}

// LoadFile reads and validates the config file at path, without falling back
// to defaults or migrating anything.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func load(path, legacyPath string) (*Config, error) {
	c, err := LoadFile(path)
	if err == nil || !os.IsNotExist(err) {
		return c, err
	}

	c, err = loadLegacy(legacyPath)
	if os.IsNotExist(err) {
		return Parse([]byte(template))
	}
	if err != nil {
		return nil, fmt.Errorf("read legacy config %s: %w", legacyPath, err)
	}

	// Only root can write the new file; unprivileged commands like "list"
	// keep working off the legacy file until someone with root runs.
	if err := save(c, path, legacyPath); err != nil {
		if !errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("migrate %s to %s: %w", legacyPath, path, err)
		}
	} else {
		log.Printf("migrated %s to %s (old file kept as %s.bak)", legacyPath, path, legacyPath)
	}
	return c, nil
}

// loadLegacy reads the JSON config used by older versions. They accepted any
// string as a domain, so IP literals are moved to IPs and anything invalid is
// dropped with a warning rather than failing the daemon after an upgrade.
func loadLegacy(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var legacy struct {
		Domains []string `json:"domains"`
		Enabled bool     `json:"enabled"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}

	c := &Config{Enabled: legacy.Enabled}
	for _, entry := range legacy.Domains {
		if _, err := c.add(entry); err != nil {
			log.Printf("dropping invalid entry %q from %s: %v", entry, path, err)
		}
	}
	return c, nil
}

// Parse decodes and validates a YAML config. Unknown keys, wrong types, and
// invalid domains or IPs are all errors. Duplicates (after normalizing) are
// dropped. A missing "enabled" key means enabled.
func Parse(data []byte) (*Config, error) {
	var raw fileFormat
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && err != io.EOF {
		return nil, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	c := &Config{Enabled: raw.Enabled == nil || *raw.Enabled}
	if len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
		c.doc = &doc
	}

	// Walk the keys in file order so errors come out sorted by line.
	var errs []error
	exclude := mappingValue(c.root(), "exclude")
	for i := 0; exclude != nil && i+1 < len(exclude.Content); i += 2 {
		key, list, normalize := exclude.Content[i].Value, &c.Domains, NormalizeDomain
		if key == "ips" {
			list, normalize = &c.IPs, NormalizeIP
		}
		for _, item := range sequenceItems(exclude.Content[i+1]) {
			v, err := normalize(item.Value)
			if err != nil {
				errs = append(errs, fmt.Errorf("line %d: exclude.%s: %w", item.Line, key, err))
			} else if !slices.Contains(*list, v) {
				*list = append(*list, v)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// Marshal renders c as YAML, reusing the document it was parsed from so
// comments and ordering survive.
func (c *Config) Marshal() ([]byte, error) {
	if c.doc == nil {
		base, err := Parse([]byte(template))
		if err != nil {
			return nil, err
		}
		c.doc = base.doc
	}

	root := c.root()
	setScalar(root, "enabled", "!!bool", fmt.Sprint(c.Enabled))
	exclude := ensureMapping(root, "exclude")
	setSequence(ensureKey(exclude, "domains"), c.Domains, NormalizeDomain)
	setSequence(ensureKey(exclude, "ips"), c.IPs, NormalizeIP)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c.doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func save(c *Config, path, legacyPath string) error {
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := writeAtomic(path, data); err != nil {
		return err
	}
	if _, err := os.Stat(legacyPath); err == nil {
		if err := os.Rename(legacyPath, legacyPath+".bak"); err != nil {
			return fmt.Errorf("move legacy config aside: %w", err)
		}
	}
	return nil
}

// writeAtomic replaces path via a temp file + rename, so a daemon reading
// concurrently never sees a half-written file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Add adds any of entries (domains or IPv4 addresses, told apart
// automatically) not already present, returning the normalized ones added.
// If any entry is invalid nothing is added.
func (c *Config) Add(entries []string) ([]string, error) {
	if err := validateEntries(entries); err != nil {
		return nil, err
	}
	var added []string
	for _, entry := range entries {
		if a, _ := c.add(entry); a != "" {
			added = append(added, a)
		}
	}
	return added, nil
}

func (c *Config) add(entry string) (string, error) {
	kind, norm, err := Classify(entry)
	if err != nil {
		return "", err
	}
	list := &c.Domains
	if kind == KindIP {
		list = &c.IPs
	}
	if slices.Contains(*list, norm) {
		return "", nil
	}
	*list = append(*list, norm)
	return norm, nil
}

// Remove drops any of entries (domains or IPv4 addresses) that are present,
// returning the normalized ones removed. If any entry is invalid nothing is
// removed.
func (c *Config) Remove(entries []string) ([]string, error) {
	if err := validateEntries(entries); err != nil {
		return nil, err
	}
	var removed []string
	for _, entry := range entries {
		kind, norm, _ := Classify(entry)
		list := &c.Domains
		if kind == KindIP {
			list = &c.IPs
		}
		if i := slices.Index(*list, norm); i >= 0 {
			*list = slices.Delete(*list, i, i+1)
			removed = append(removed, norm)
		}
	}
	return removed, nil
}

func validateEntries(entries []string) error {
	var errs []error
	for _, entry := range entries {
		if _, _, err := Classify(entry); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Config) root() *yaml.Node {
	if c.doc == nil {
		return nil
	}
	return c.doc.Content[0]
}

// mappingValue returns the value node for key in mapping m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func sequenceItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// ensureKey returns the value node for key in m, appending an empty one if
// key is missing.
func ensureKey(m *yaml.Node, key string) *yaml.Node {
	if v := mappingValue(m, key); v != nil {
		return v
	}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return v
}

func ensureMapping(m *yaml.Node, key string) *yaml.Node {
	v := ensureKey(m, key)
	if v.Kind != yaml.MappingNode {
		*v = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: v.HeadComment, LineComment: v.LineComment}
	}
	return v
}

func setScalar(m *yaml.Node, key, tag, value string) {
	v := ensureKey(m, key)
	v.Kind, v.Tag, v.Value, v.Style, v.Content = yaml.ScalarNode, tag, value, 0, nil
}

// setSequence makes n a sequence of items, reusing the existing node (and
// so its comments) for every item that was already in it.
func setSequence(n *yaml.Node, items []string, normalize func(string) (string, error)) {
	existing := make(map[string]*yaml.Node)
	for _, item := range sequenceItems(n) {
		if norm, err := normalize(item.Value); err == nil {
			existing[norm] = item
		}
	}

	content := make([]*yaml.Node, 0, len(items))
	for _, item := range items {
		node, ok := existing[item]
		if !ok {
			node = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item}
		}
		content = append(content, node)
	}

	n.Kind, n.Tag, n.Value, n.Content = yaml.SequenceNode, "!!seq", "", content
	// An empty block sequence would render as "null"; keep it a visible [].
	if len(content) == 0 {
		n.Style = yaml.FlowStyle
	} else {
		n.Style = 0
	}
}

// Kind tells which list an exclusion entry belongs to.
type Kind int

const (
	KindDomain Kind = iota
	KindIP
)

// Classify decides whether entry is an IPv4 address or a domain and returns
// it normalized. IPv6 addresses and CIDR ranges are rejected explicitly
// rather than being mistaken for (invalid) domains.
func Classify(entry string) (Kind, string, error) {
	if looksLikeIP(entry) {
		ip, err := NormalizeIP(entry)
		return KindIP, ip, err
	}
	d, err := NormalizeDomain(entry)
	return KindDomain, d, err
}

func looksLikeIP(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ":") || strings.Contains(s, "/") {
		return true
	}
	return s != "" && strings.Trim(s, "0123456789.") == ""
}

// NormalizeIP validates s as a single IPv4 address and returns its canonical
// form.
func NormalizeIP(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.Contains(s, "/"):
		return "", fmt.Errorf("%q: CIDR ranges are not supported, list individual IPv4 addresses", s)
	case strings.Contains(s, ":"):
		return "", fmt.Errorf("%q: IPv6 is not supported, only IPv4 addresses", s)
	}
	ip := parseIPv4(s)
	if ip == "" {
		return "", fmt.Errorf("%q is not a valid IPv4 address", s)
	}
	return ip, nil
}

// NormalizeDomain validates s as a DNS name and returns it lowercased,
// without a trailing dot.
func NormalizeDomain(s string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if d == "" {
		return "", errors.New("empty domain")
	}
	if looksLikeIP(d) {
		return "", fmt.Errorf("%q is an IP address, not a domain (put it under exclude.ips)", s)
	}
	if len(d) > 253 {
		return "", fmt.Errorf("%q: domain longer than 253 characters", s)
	}
	for _, label := range strings.Split(d, ".") {
		if !validLabel(label) {
			return "", fmt.Errorf("%q is not a valid domain name", s)
		}
	}
	return d, nil
}

// parseIPv4 returns the canonical form of s if it is a plain dotted-quad
// IPv4 address, or "" otherwise.
func parseIPv4(s string) string {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return ""
	}
	return addr.String()
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, r := range l {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
