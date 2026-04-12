package server

import "time"

// cleanupDeadline is the maximum time to wait for inflight requests to drain
// during connection cleanup.
const cleanupDeadline = 5 * time.Second
