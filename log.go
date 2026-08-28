package main

import (
	"fmt"
	"io"
	"os"
)

// errOut is the sink for every message smexec produces. Swapped in tests to
// assert that no secret value is ever logged.
var errOut io.Writer = os.Stderr

// logf writes a diagnostic line. Key names and counts only: never values, and
// never to stdout, which consumers parse as container output.
func logf(format string, args ...any) {
	fmt.Fprintf(errOut, "smexec: "+format+"\n", args...)
}
