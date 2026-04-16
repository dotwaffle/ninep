//go:build linux

package passthrough

import "golang.org/x/sys/unix"

// oPath is the O_PATH open(2) flag. On Linux, golang.org/x/sys exports
// unix.O_PATH directly.
const oPath = unix.O_PATH
