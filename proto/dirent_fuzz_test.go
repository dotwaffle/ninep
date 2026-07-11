package proto

import (
	"bytes"
	"reflect"
	"testing"
)

// FuzzDirentRoundTrip verifies both directions of the dirent codec from one
// harness: any byte stream ParseDirents accepts in full must re-encode to
// identical bytes, and re-parse to identical entries. Seeded with encoded
// output so the fuzzer starts from structurally valid streams.
func FuzzDirentRoundTrip(f *testing.F) {
	seedEntries := [][]Dirent{
		{},
		{{QID: QID{Type: QTFILE, Version: 1, Path: 42}, Offset: 1, Type: DT_REG, Name: "hello"}},
		{
			{QID: QID{Type: QTDIR, Path: 1}, Offset: 1, Type: DT_DIR, Name: "."},
			{QID: QID{Type: QTDIR, Path: 2}, Offset: 2, Type: DT_DIR, Name: ".."},
			{QID: QID{Type: QTFILE, Path: 3}, Offset: 3, Type: DT_LNK, Name: "link"},
		},
		{{QID: QID{}, Offset: 0, Type: 0, Name: ""}},
	}
	for _, entries := range seedEntries {
		data, _ := EncodeDirents(entries, 64*1024)
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseDirents(data)
		if err != nil {
			return // Malformed input is fine -- the invariant is no panics.
		}

		// A fully-parsed stream must re-encode to the identical bytes.
		dst := make([]byte, len(data))
		n, count := EncodeDirentsInto(dst, parsed)
		if count != len(parsed) {
			t.Fatalf("re-encode fit %d of %d parsed entries", count, len(parsed))
		}
		if !bytes.Equal(dst[:n], data) {
			t.Fatalf("re-encode mismatch:\n  got:  %x\n  want: %x", dst[:n], data)
		}

		// And re-parse to identical entries.
		parsed2, err := ParseDirents(dst[:n])
		if err != nil {
			t.Fatalf("re-parse failed: %v", err)
		}
		if !reflect.DeepEqual(parsed, parsed2) {
			t.Fatalf("re-parse mismatch:\n  got:  %+v\n  want: %+v", parsed2, parsed)
		}
	})
}

func TestParseDirentsMalformed(t *testing.T) {
	t.Parallel()

	valid, _ := EncodeDirents([]Dirent{
		{QID: QID{Type: QTFILE, Path: 1}, Offset: 1, Type: DT_REG, Name: "abc"},
		{QID: QID{Type: QTDIR, Path: 2}, Offset: 2, Type: DT_DIR, Name: "d"},
	}, 4096)

	t.Run("valid stream parses fully", func(t *testing.T) {
		t.Parallel()
		got, err := ParseDirents(valid)
		if err != nil {
			t.Fatalf("ParseDirents: %v", err)
		}
		if len(got) != 2 || got[0].Name != "abc" || got[1].Name != "d" {
			t.Errorf("parsed %+v", got)
		}
	})

	t.Run("truncated header returns partial entries and error", func(t *testing.T) {
		t.Parallel()
		got, err := ParseDirents(valid[:len(valid)-10])
		if err == nil {
			t.Fatal("expected error for truncated stream")
		}
		if len(got) != 1 || got[0].Name != "abc" {
			t.Errorf("partial result = %+v, want the first entry", got)
		}
	})

	t.Run("name length overrun returns error", func(t *testing.T) {
		t.Parallel()
		bad := bytes.Clone(valid)
		// Corrupt the first entry's 2-byte name length (offset 22) to claim
		// more bytes than remain in the stream.
		bad[22] = 0xFF
		bad[23] = 0xFF
		if _, err := ParseDirents(bad); err == nil {
			t.Fatal("expected error for name length overrun")
		}
	})
}
