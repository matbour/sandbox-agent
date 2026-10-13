package job

import (
	"bytes"
	"errors"
)

// errSecretLeak stops a run whose output contains a secret the host gave the guest (D43). The
// output is not logged, published or pushed.
var errSecretLeak = errors.New("the agent's output contains the subscription token: run rejected, rotate the token")

// leaks reports whether any of outs contains secret. An empty secret never leaks.
func leaks(secret string, outs ...[]byte) bool {
	if len(secret) < 8 {
		return false
	}

	for _, o := range outs {
		if bytes.Contains(o, []byte(secret)) {
			return true
		}
	}

	return false
}
