// Command sandbox runs a command inside an isolated environment built from Linux primitives.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "sandbox: not implemented yet (%s/%s)\n", runtime.GOOS, runtime.GOARCH)
	os.Exit(1)
}
