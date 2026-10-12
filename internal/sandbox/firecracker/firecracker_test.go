package firecracker

import (
	"strings"
	"testing"
)

func TestParseMemory(t *testing.T) {
	for in, want := range map[string]int{"": 1024, "1g": 1024, "6G": 6144, "512m": 512, "300": 300} {
		if got, err := parseMemory(in); err != nil || got != want {
			t.Errorf("parseMemory(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	for _, bad := range []string{"x", "-1g", "0"} {
		if _, err := parseMemory(bad); err == nil {
			t.Errorf("parseMemory(%q) accepted", bad)
		}
	}
}

func TestBootArgs(t *testing.T) {
	p := NewWith(Config{Subnet: "10.230.0", DNS: "1.1.1.1"})

	with := p.bootArgs("job-1", &tapLease{name: "fc-tap2", index: 2})
	if !strings.Contains(with, "ip=10.230.0.12::10.230.0.1:255.255.255.0::eth0:off") ||
		!strings.Contains(with, "sandbox.hostname=job-1") || !strings.HasSuffix(with, `exec /run/agent/guest-agent"`) {
		t.Errorf("boot args: %s", with)
	}

	if without := p.bootArgs("job-1", nil); strings.Contains(without, "ip=") {
		t.Errorf("ip= without a tap: %s", without)
	}
}

func TestCapped(t *testing.T) {
	c := &capped{max: 4}

	n, err := c.Write([]byte("abcdef"))
	if n != 6 || err != nil || c.String() != "abcd" || !c.truncated {
		t.Errorf("n %d err %v buf %q truncated %v", n, err, c.String(), c.truncated)
	}
}

func TestTapIndex(t *testing.T) {
	if tapIndex("fc-tap3", 0) != 3 || tapIndex("other", 5) != 5 {
		t.Error("tap index")
	}
}
