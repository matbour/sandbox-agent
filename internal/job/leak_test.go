package job

import "testing"

func TestLeaks(t *testing.T) {
	const tok = "sk-ant-oat01-example-token"

	if !leaks(tok, []byte("ok"), []byte("diff +"+tok)) {
		t.Error("token in the patch not detected")
	}

	if leaks(tok, []byte("sk-ant-oat01-other")) || leaks("", []byte("anything")) {
		t.Error("false positive")
	}
}
