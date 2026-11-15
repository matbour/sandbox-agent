// Command sandbox runs coding agents in isolated sandboxes, on their own: no tracker, no forge.
//
//	sandbox task -repo <git repository> -task <task.md> [-out <dir>]
//
// The contract (D52): a Git repository and a task in Markdown go in; a patch against the
// repository's HEAD, the agent's events and its result come out. The agent runs in a fresh sandbox
// (Firecracker, gVisor) with the repository's files and no Git metadata, credentials or host
// access; what it leaves is applied to a clean clone that never entered the sandbox, and the
// patch is taken there. agentd's Linear and GitHub flow is one client of the same pieces.
//
// sandbox run, the layer 1 runtime of the roadmap (M3), is not written yet.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "task":
		os.Exit(runTask(os.Args[2:]))
	case "run":
		fmt.Fprintf(os.Stderr, "sandbox run: not implemented yet (%s/%s, roadmap M3)\n", runtime.GOOS, runtime.GOARCH)
		os.Exit(1)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "sandbox: unknown command %q\n", os.Args[1])
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  sandbox task -repo <git repository> -task <task.md> [flags]   run a coding agent, get a patch
  sandbox run -- <command>                                      the layer 1 runtime (not yet)

Run "sandbox task -h" for the task flags.
`)
	os.Exit(2)
}
