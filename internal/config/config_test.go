package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`
enabled: false
exclude:
  domains:
    - Example.COM.
    - example.com
    - sub.example.org
  ips:
    - 104.16.12.34
    - 104.16.12.34/32
    - 160.79.104.0/23
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled {
		t.Error("Enabled = true, want false")
	}
	if want := []string{"example.com", "sub.example.org"}; !slices.Equal(c.Domains, want) {
		t.Errorf("Domains = %v, want %v", c.Domains, want)
	}
	if want := []string{"104.16.12.34", "160.79.104.0/23"}; !slices.Equal(c.IPs, want) {
		t.Errorf("IPs = %v, want %v", c.IPs, want)
	}
}

func TestParseDefaults(t *testing.T) {
	for _, in := range []string{"", "# only a comment\n", "exclude:\n  domains:\n"} {
		c, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if !c.Enabled || len(c.Domains) != 0 || len(c.IPs) != 0 {
			t.Errorf("Parse(%q) = %+v, want enabled and empty", in, c)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"unknown key", "exclude:\n  domain:\n    - a.com\n", "field domain not found"},
		{"wrong type", "enabled: maybe\n", "cannot unmarshal"},
		{"ipv6", "exclude:\n  ips:\n    - 2001:db8::1\n", "line 3: exclude.ips: \"2001:db8::1\": IPv6 is not supported"},
		{"cidr host bits", "exclude:\n  ips:\n    - 10.1.2.3/8\n", "has host bits set (did you mean 10.0.0.0/8?)"},
		{"cidr zero bits", "exclude:\n  ips:\n    - 0.0.0.0/0\n", "would replace the default route"},
		{"cidr bad bits", "exclude:\n  ips:\n    - 10.0.0.0/33\n", "not a valid IPv4 CIDR range"},
		{"cidr as domain", "exclude:\n  domains:\n    - 10.0.0.0/8\n", "put it under exclude.ips"},
		{"bad ip", "exclude:\n  ips:\n    - 1.2.3.256\n", "not a valid IPv4 address"},
		{"ip as domain", "exclude:\n  domains:\n    - 1.2.3.4\n", "put it under exclude.ips"},
		{"bad domain", "exclude:\n  domains:\n    - -bad-.com\n", "not a valid domain name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseReportsAllErrors(t *testing.T) {
	_, err := Parse([]byte("exclude:\n  domains:\n    - bad..com\n  ips:\n    - nope.1\n    - ::1\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"line 3", "line 5", "line 6"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want mention of %s", err, want)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		in       string
		kind     Kind
		norm     string
		wantsErr bool
	}{
		{"example.com", KindDomain, "example.com", false},
		{"Example.com.", KindDomain, "example.com", false},
		{"_dmarc.example.com", KindDomain, "_dmarc.example.com", false},
		{"1.2.3.4", KindIP, "1.2.3.4", false},
		{" 1.2.3.4 ", KindIP, "1.2.3.4", false},
		{"1.2.3", KindIP, "", true},
		{"::1", KindIP, "", true},
		{"10.0.0.0/8", KindIP, "10.0.0.0/8", false},
		{" 160.79.104.0/23 ", KindIP, "160.79.104.0/23", false},
		{"1.2.3.4/32", KindIP, "1.2.3.4", false},
		{"10.0.0.1/8", KindIP, "", true},
		{"::/0", KindIP, "", true},
		{"no spaces.com", KindDomain, "", true},
	}
	for _, tt := range tests {
		kind, norm, err := Classify(tt.in)
		if (err != nil) != tt.wantsErr || kind != tt.kind || norm != tt.norm {
			t.Errorf("Classify(%q) = %v, %q, %v; want %v, %q, err=%v", tt.in, kind, norm, err, tt.kind, tt.norm, tt.wantsErr)
		}
	}
}

func TestAddRemove(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}

	added, err := c.Add([]string{"a.com", "1.2.3.4", "A.com"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.com", "1.2.3.4"}; !slices.Equal(added, want) {
		t.Errorf("added = %v, want %v", added, want)
	}

	if _, err := c.Add([]string{"b.com", "::1"}); err == nil {
		t.Error("Add with an invalid entry succeeded")
	}
	if slices.Contains(c.Domains, "b.com") {
		t.Error("Add with an invalid entry still added the valid ones")
	}

	removed, err := c.Remove([]string{"1.2.3.4", "missing.com"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.2.3.4"}; !slices.Equal(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if !slices.Equal(c.Domains, []string{"a.com"}) || len(c.IPs) != 0 {
		t.Errorf("after remove: domains=%v ips=%v", c.Domains, c.IPs)
	}
}

func TestMarshalPreservesComments(t *testing.T) {
	in := `# my team's list
enabled: true
exclude:
  domains:
    - keep.com # needed for VPN-less access
    - drop.com
  ips:
    - 1.1.1.1
`
	c, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = false
	if _, err := c.Remove([]string{"drop.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add([]string{"new.com", "2.2.2.2"}); err != nil {
		t.Fatal(err)
	}

	out, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want := `# my team's list
enabled: false
exclude:
  domains:
    - keep.com # needed for VPN-less access
    - new.com
  ips:
    - 1.1.1.1
    - 2.2.2.2
`
	if string(out) != want {
		t.Errorf("Marshal:\n%s\nwant:\n%s", out, want)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range [][]string{{"a.com", "1.2.3.4"}, nil} {
		if step != nil {
			if _, err := c.Add(step); err != nil {
				t.Fatal(err)
			}
		} else if _, err := c.Remove([]string{"a.com", "1.2.3.4"}); err != nil {
			t.Fatal(err)
		}

		out, err := c.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(out)
		if err != nil {
			t.Fatalf("re-parse:\n%s\n%v", out, err)
		}
		if !slices.Equal(back.Domains, c.Domains) || !slices.Equal(back.IPs, c.IPs) || back.Enabled != c.Enabled {
			t.Errorf("round trip mismatch:\n%s", out)
		}
		if !strings.Contains(string(out), "# netbird-excluder config") {
			t.Errorf("template comments lost:\n%s", out)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	c, err := load(filepath.Join(dir, "config.yaml"), filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || len(c.Domains) != 0 || len(c.IPs) != 0 {
		t.Errorf("got %+v, want enabled and empty", c)
	}
}

func TestLoadMigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	path, legacy := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "config.json")
	if err := os.WriteFile(legacy, []byte(`{"domains":["a.com","1.2.3.4","not valid"],"enabled":false}`), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := load(path, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled || !slices.Equal(c.Domains, []string{"a.com"}) || !slices.Equal(c.IPs, []string{"1.2.3.4"}) {
		t.Errorf("got enabled=%v domains=%v ips=%v", c.Enabled, c.Domains, c.IPs)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file still present: %v", err)
	}
	if _, err := os.Stat(legacy + ".bak"); err != nil {
		t.Errorf("legacy backup missing: %v", err)
	}
	onDisk, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Enabled || !slices.Equal(onDisk.Domains, c.Domains) || !slices.Equal(onDisk.IPs, c.IPs) {
		t.Errorf("migrated file = %+v, want %+v", onDisk, c)
	}
}

func TestSaveAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	c, _ := Parse(nil)
	if err := save(c, path, filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Errorf("dir contents = %v, want only config.yaml", entries)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}
