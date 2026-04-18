package client

// syncStub is the Phase 20 placeholder for [File.Sync]. Returns nil
// unconditionally -- no wire op, no state change. Kept in its own
// file so Phase 21 can replace this single function body (rename the
// file to sync.go and swap the return for a Tgetattr (.L) / Tstat
// (.u) round-trip that populates f.cachedSize) without touching
// file.go or the sync_test.go fixture.
//
// Contract locked by 20-05 plan Q6 resolution: callers that depend on
// File.Seek with io.SeekEnd treat Phase 20's Sync as "no effect".
//
// # Phase 21 expected shape
//
// On .L, Tgetattr(fid, AttrSize) yields Rgetattr.Size which maps
// directly to f.cachedSize. On .u, Tstat(fid) yields an Rstat
// carrying a proto.Stat whose Length field populates f.cachedSize.
// Both branches take f.mu around the write so the SeekEnd read sees
// a coherent value.
//
// TODO(phase-21): replace with Tgetattr (.L) / Tstat (.u) call. See
// .planning/phases/20/20-RESEARCH.md §9 Pitfall 5 for the expected
// shape (AttrSize bitmask, populate f.cachedSize under f.mu).
func (f *File) syncStub() error {
	return nil
}
