package proto

import "testing"

// TestDTConstants verifies the exported DT_* constants match Linux dirent.h
// values (verified against glibc /usr/include/dirent.h and x/sys/unix
// zerrors_linux.go). The v9fs kernel client passes Dirent.Type verbatim to
// dir_emit() in .L mode, so these values are the kernel ABI contract.
func TestDTConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  uint8
		want uint8
	}{
		{"DT_UNKNOWN", DT_UNKNOWN, 0},
		{"DT_FIFO", DT_FIFO, 1},
		{"DT_CHR", DT_CHR, 2},
		{"DT_DIR", DT_DIR, 4},
		{"DT_BLK", DT_BLK, 6},
		{"DT_REG", DT_REG, 8},
		{"DT_LNK", DT_LNK, 10},
		{"DT_SOCK", DT_SOCK, 12},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestQIDTypeToDT verifies the QID-type-to-Linux-DT_* mapping used by
// synthetic filesystems (memfs, fstest) that derive dirent type bytes from
// QID metadata rather than stat-derived mode bits.
func TestQIDTypeToDT(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   QIDType
		want uint8
	}{
		{"file", QTFILE, DT_REG},
		{"dir", QTDIR, DT_DIR},
		{"symlink", QTSYMLINK, DT_LNK},
		{"dir+append", QTDIR | QTAPPEND, DT_DIR}, // bitmask: QTDIR high bit preserved under OR
		{"zero", 0, DT_REG},
	}
	for _, c := range cases {
		if got := QIDTypeToDT(c.in); got != c.want {
			t.Errorf("QIDTypeToDT(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}
