// Package p9u implements the 9P2000.u-specific message types and top-level
// Encode/Decode framing for the 9P2000.u wire protocol.
package p9u

import (
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/proto"
)

// 9P2000.u file mode constants for special file types.
const (
	DMSYMLINK   proto.FileMode = 0x02000000 // Symbolic link.
	DMDEVICE    proto.FileMode = 0x00800000 // Device file.
	DMNAMEDPIPE proto.FileMode = 0x00200000 // Named pipe (FIFO).
	DMSOCKET    proto.FileMode = 0x00100000 // Socket.
	DMSETUID    proto.FileMode = 0x00080000 // Set-user-ID on execute.
	DMSETGID    proto.FileMode = 0x00040000 // Set-group-ID on execute.
)

// Rerror is the 9P2000.u error response. It carries both a human-readable
// error string and a numeric errno, unlike base 9P2000 which has only the
// string. Wire body: ename[s] + errno[4].
type Rerror struct {
	Ename string
	Errno proto.Errno
}

// Type returns proto.TypeRerror.
func (m *Rerror) Type() proto.MessageType { return proto.TypeRerror }

// EncodeTo writes the Rerror body: ename[s] + errno[4].
func (m *Rerror) EncodeTo(w io.Writer) error {
	if err := proto.WriteString(w, m.Ename); err != nil {
		return fmt.Errorf("encode rerror ename: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Errno)); err != nil {
		return fmt.Errorf("encode rerror errno: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rerror body: ename[s] + errno[4].
func (m *Rerror) DecodeFrom(r io.Reader) error {
	var err error
	if m.Ename, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode rerror ename: %w", err)
	}
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode rerror errno: %w", err)
	}
	m.Errno = proto.Errno(v)
	return nil
}

// Topen requests opening the file identified by Fid with the given Mode.
// Wire body: fid[4] + mode[1].
type Topen struct {
	Fid  proto.Fid
	Mode uint8
}

// Type returns proto.TypeTopen.
func (m *Topen) Type() proto.MessageType { return proto.TypeTopen }

// EncodeTo writes the Topen body: fid[4] + mode[1].
func (m *Topen) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode topen fid: %w", err)
	}
	if err := proto.WriteUint8(w, m.Mode); err != nil {
		return fmt.Errorf("encode topen mode: %w", err)
	}
	return nil
}

// DecodeFrom reads the Topen body: fid[4] + mode[1].
func (m *Topen) DecodeFrom(r io.Reader) error {
	fid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode topen fid: %w", err)
	}
	m.Fid = proto.Fid(fid)
	if m.Mode, err = proto.ReadUint8(r); err != nil {
		return fmt.Errorf("decode topen mode: %w", err)
	}
	return nil
}

// Ropen is the server's response to Topen, providing the file's QID and I/O
// unit size. Wire body: qid[13] + iounit[4].
type Ropen struct {
	QID    proto.QID
	IOUnit uint32
}

// Type returns proto.TypeRopen.
func (m *Ropen) Type() proto.MessageType { return proto.TypeRopen }

// EncodeTo writes the Ropen body: qid[13] + iounit[4].
func (m *Ropen) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode ropen qid: %w", err)
	}
	if err := proto.WriteUint32(w, m.IOUnit); err != nil {
		return fmt.Errorf("encode ropen iounit: %w", err)
	}
	return nil
}

// DecodeFrom reads the Ropen body: qid[13] + iounit[4].
func (m *Ropen) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode ropen qid: %w", err)
	}
	if m.IOUnit, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode ropen iounit: %w", err)
	}
	return nil
}

// Tcreate requests creating a new file. The Extension field is a 9P2000.u
// addition used for symlink targets ("b major minor" for devices, etc.).
// Wire body: fid[4] + name[s] + perm[4] + mode[1] + extension[s].
type Tcreate struct {
	Fid       proto.Fid
	Name      string
	Perm      proto.FileMode
	Mode      uint8
	Extension string
}

// Type returns proto.TypeTcreate.
func (m *Tcreate) Type() proto.MessageType { return proto.TypeTcreate }

// EncodeTo writes the Tcreate body: fid[4] + name[s] + perm[4] + mode[1] + extension[s].
func (m *Tcreate) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tcreate fid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tcreate name: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Perm)); err != nil {
		return fmt.Errorf("encode tcreate perm: %w", err)
	}
	if err := proto.WriteUint8(w, m.Mode); err != nil {
		return fmt.Errorf("encode tcreate mode: %w", err)
	}
	if err := proto.WriteString(w, m.Extension); err != nil {
		return fmt.Errorf("encode tcreate extension: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tcreate body: fid[4] + name[s] + perm[4] + mode[1] + extension[s].
func (m *Tcreate) DecodeFrom(r io.Reader) error {
	fid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tcreate fid: %w", err)
	}
	m.Fid = proto.Fid(fid)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tcreate name: %w", err)
	}
	perm, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tcreate perm: %w", err)
	}
	m.Perm = proto.FileMode(perm)
	if m.Mode, err = proto.ReadUint8(r); err != nil {
		return fmt.Errorf("decode tcreate mode: %w", err)
	}
	if m.Extension, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tcreate extension: %w", err)
	}
	return nil
}

// Rcreate is the server's response to Tcreate, providing the new file's QID
// and I/O unit size. Wire body: qid[13] + iounit[4].
type Rcreate struct {
	QID    proto.QID
	IOUnit uint32
}

// Type returns proto.TypeRcreate.
func (m *Rcreate) Type() proto.MessageType { return proto.TypeRcreate }

// EncodeTo writes the Rcreate body: qid[13] + iounit[4].
func (m *Rcreate) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rcreate qid: %w", err)
	}
	if err := proto.WriteUint32(w, m.IOUnit); err != nil {
		return fmt.Errorf("encode rcreate iounit: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rcreate body: qid[13] + iounit[4].
func (m *Rcreate) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rcreate qid: %w", err)
	}
	if m.IOUnit, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rcreate iounit: %w", err)
	}
	return nil
}

// Stat is the 9P2000.u stat structure. It is not a Message (it does not appear
// as a top-level wire message), but it provides EncodeTo/DecodeFrom methods
// for serialization as part of Rstat and Twstat messages.
//
// 9P2000.u extends the base stat with Extension, NUid, NGid, and NMuid fields.
//
// Wire format after the 2-byte size prefix:
//
//	type[2] dev[4] qid[13] mode[4] atime[4] mtime[4] length[8]
//	name[s] uid[s] gid[s] muid[s] extension[s] n_uid[4] n_gid[4] n_muid[4]
type Stat struct {
	Size      uint16 // Total size of stat entry, excluding this field itself.
	Type      uint16
	Dev       uint32
	QID       proto.QID
	Mode      proto.FileMode
	Atime     uint32
	Mtime     uint32
	Length    uint64
	Name      string
	UID       string
	GID       string
	MUID      string
	Extension string // 9P2000.u: symlink target, "b major minor" for devices, etc.
	NUid      uint32 // 9P2000.u: numeric UID.
	NGid      uint32 // 9P2000.u: numeric GID.
	NMuid     uint32 // 9P2000.u: numeric MUID.
}

// EncodedSize calculates the total encoded size of the stat body, excluding the
// 2-byte size prefix itself. This value is written as the size prefix.
func (s *Stat) EncodedSize() uint16 {
	// Fixed fields: type[2] + dev[4] + qid[13] + mode[4] + atime[4] + mtime[4] + length[8] = 39
	// String fields: each has 2-byte length prefix + data
	// Extension fields: extension[s] + n_uid[4] + n_gid[4] + n_muid[4] = s + 12
	size := uint16(39)
	size += 2 + uint16(len(s.Name))
	size += 2 + uint16(len(s.UID))
	size += 2 + uint16(len(s.GID))
	size += 2 + uint16(len(s.MUID))
	size += 2 + uint16(len(s.Extension))
	size += 12 // n_uid[4] + n_gid[4] + n_muid[4]
	return size
}

// EncodeTo writes the stat to w: size[2] + body fields.
func (s *Stat) EncodeTo(w io.Writer) error {
	s.Size = s.EncodedSize()
	if err := proto.WriteUint16(w, s.Size); err != nil {
		return fmt.Errorf("encode stat size: %w", err)
	}
	if err := proto.WriteUint16(w, s.Type); err != nil {
		return fmt.Errorf("encode stat type: %w", err)
	}
	if err := proto.WriteUint32(w, s.Dev); err != nil {
		return fmt.Errorf("encode stat dev: %w", err)
	}
	if err := proto.WriteQID(w, s.QID); err != nil {
		return fmt.Errorf("encode stat qid: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(s.Mode)); err != nil {
		return fmt.Errorf("encode stat mode: %w", err)
	}
	if err := proto.WriteUint32(w, s.Atime); err != nil {
		return fmt.Errorf("encode stat atime: %w", err)
	}
	if err := proto.WriteUint32(w, s.Mtime); err != nil {
		return fmt.Errorf("encode stat mtime: %w", err)
	}
	if err := proto.WriteUint64(w, s.Length); err != nil {
		return fmt.Errorf("encode stat length: %w", err)
	}
	if err := proto.WriteString(w, s.Name); err != nil {
		return fmt.Errorf("encode stat name: %w", err)
	}
	if err := proto.WriteString(w, s.UID); err != nil {
		return fmt.Errorf("encode stat uid: %w", err)
	}
	if err := proto.WriteString(w, s.GID); err != nil {
		return fmt.Errorf("encode stat gid: %w", err)
	}
	if err := proto.WriteString(w, s.MUID); err != nil {
		return fmt.Errorf("encode stat muid: %w", err)
	}
	if err := proto.WriteString(w, s.Extension); err != nil {
		return fmt.Errorf("encode stat extension: %w", err)
	}
	if err := proto.WriteUint32(w, s.NUid); err != nil {
		return fmt.Errorf("encode stat n_uid: %w", err)
	}
	if err := proto.WriteUint32(w, s.NGid); err != nil {
		return fmt.Errorf("encode stat n_gid: %w", err)
	}
	if err := proto.WriteUint32(w, s.NMuid); err != nil {
		return fmt.Errorf("encode stat n_muid: %w", err)
	}
	return nil
}

// DecodeFrom reads the stat from r: size[2] + body fields.
func (s *Stat) DecodeFrom(r io.Reader) error {
	size, err := proto.ReadUint16(r)
	if err != nil {
		return fmt.Errorf("decode stat size: %w", err)
	}
	s.Size = size

	// Bound the read to the declared stat size to prevent over-reading.
	lr := io.LimitReader(r, int64(size))

	if s.Type, err = proto.ReadUint16(lr); err != nil {
		return fmt.Errorf("decode stat type: %w", err)
	}
	if s.Dev, err = proto.ReadUint32(lr); err != nil {
		return fmt.Errorf("decode stat dev: %w", err)
	}
	if s.QID, err = proto.ReadQID(lr); err != nil {
		return fmt.Errorf("decode stat qid: %w", err)
	}
	mode, err := proto.ReadUint32(lr)
	if err != nil {
		return fmt.Errorf("decode stat mode: %w", err)
	}
	s.Mode = proto.FileMode(mode)
	if s.Atime, err = proto.ReadUint32(lr); err != nil {
		return fmt.Errorf("decode stat atime: %w", err)
	}
	if s.Mtime, err = proto.ReadUint32(lr); err != nil {
		return fmt.Errorf("decode stat mtime: %w", err)
	}
	if s.Length, err = proto.ReadUint64(lr); err != nil {
		return fmt.Errorf("decode stat length: %w", err)
	}
	if s.Name, err = proto.ReadString(lr); err != nil {
		return fmt.Errorf("decode stat name: %w", err)
	}
	if s.UID, err = proto.ReadString(lr); err != nil {
		return fmt.Errorf("decode stat uid: %w", err)
	}
	if s.GID, err = proto.ReadString(lr); err != nil {
		return fmt.Errorf("decode stat gid: %w", err)
	}
	if s.MUID, err = proto.ReadString(lr); err != nil {
		return fmt.Errorf("decode stat muid: %w", err)
	}
	if s.Extension, err = proto.ReadString(lr); err != nil {
		return fmt.Errorf("decode stat extension: %w", err)
	}
	if s.NUid, err = proto.ReadUint32(lr); err != nil {
		return fmt.Errorf("decode stat n_uid: %w", err)
	}
	if s.NGid, err = proto.ReadUint32(lr); err != nil {
		return fmt.Errorf("decode stat n_gid: %w", err)
	}
	if s.NMuid, err = proto.ReadUint32(lr); err != nil {
		return fmt.Errorf("decode stat n_muid: %w", err)
	}
	return nil
}

// Tstat requests the stat of the file identified by Fid.
// Wire body: fid[4].
type Tstat struct {
	Fid proto.Fid
}

// Type returns proto.TypeTstat.
func (m *Tstat) Type() proto.MessageType { return proto.TypeTstat }

// EncodeTo writes the Tstat body: fid[4].
func (m *Tstat) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tstat fid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tstat body: fid[4].
func (m *Tstat) DecodeFrom(r io.Reader) error {
	fid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tstat fid: %w", err)
	}
	m.Fid = proto.Fid(fid)
	return nil
}

// Rstat is the server's response to Tstat, carrying the file's stat structure.
// Wire body: nstat[2] + stat_data[nstat]. The nstat value is the total byte
// count of the stat encoding including the stat's own 2-byte size prefix.
type Rstat struct {
	Stat Stat
}

// Type returns proto.TypeRstat.
func (m *Rstat) Type() proto.MessageType { return proto.TypeRstat }

// EncodeTo writes the Rstat body: nstat[2] + stat_data[nstat].
func (m *Rstat) EncodeTo(w io.Writer) error {
	// nstat = stat's 2-byte size prefix + stat body.
	nstat := uint16(2) + m.Stat.EncodedSize()
	if err := proto.WriteUint16(w, nstat); err != nil {
		return fmt.Errorf("encode rstat nstat: %w", err)
	}
	if err := m.Stat.EncodeTo(w); err != nil {
		return fmt.Errorf("encode rstat stat: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rstat body: nstat[2] + stat_data[nstat].
func (m *Rstat) DecodeFrom(r io.Reader) error {
	nstat, err := proto.ReadUint16(r)
	if err != nil {
		return fmt.Errorf("decode rstat nstat: %w", err)
	}
	// Bound the stat read to the declared nstat bytes.
	lr := io.LimitReader(r, int64(nstat))
	if err := m.Stat.DecodeFrom(lr); err != nil {
		return fmt.Errorf("decode rstat stat: %w", err)
	}
	return nil
}

// Twstat requests modifying the stat of the file identified by Fid.
// Wire body: nstat[2] + stat_data[nstat].
type Twstat struct {
	Fid  proto.Fid
	Stat Stat
}

// Type returns proto.TypeTwstat.
func (m *Twstat) Type() proto.MessageType { return proto.TypeTwstat }

// EncodeTo writes the Twstat body: fid[4] + nstat[2] + stat_data[nstat].
func (m *Twstat) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode twstat fid: %w", err)
	}
	nstat := uint16(2) + m.Stat.EncodedSize()
	if err := proto.WriteUint16(w, nstat); err != nil {
		return fmt.Errorf("encode twstat nstat: %w", err)
	}
	if err := m.Stat.EncodeTo(w); err != nil {
		return fmt.Errorf("encode twstat stat: %w", err)
	}
	return nil
}

// DecodeFrom reads the Twstat body: fid[4] + nstat[2] + stat_data[nstat].
func (m *Twstat) DecodeFrom(r io.Reader) error {
	fid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode twstat fid: %w", err)
	}
	m.Fid = proto.Fid(fid)
	nstat, err := proto.ReadUint16(r)
	if err != nil {
		return fmt.Errorf("decode twstat nstat: %w", err)
	}
	lr := io.LimitReader(r, int64(nstat))
	if err := m.Stat.DecodeFrom(lr); err != nil {
		return fmt.Errorf("decode twstat stat: %w", err)
	}
	return nil
}

// Rwstat confirms a wstat operation. It has an empty body.
type Rwstat struct{}

// Type returns proto.TypeRwstat.
func (m *Rwstat) Type() proto.MessageType { return proto.TypeRwstat }

// EncodeTo writes nothing; Rwstat has an empty body.
func (m *Rwstat) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rwstat has an empty body.
func (m *Rwstat) DecodeFrom(_ io.Reader) error { return nil }

// Compile-time interface compliance checks.
var (
	_ proto.Message = (*Rerror)(nil)
	_ proto.Message = (*Topen)(nil)
	_ proto.Message = (*Ropen)(nil)
	_ proto.Message = (*Tcreate)(nil)
	_ proto.Message = (*Rcreate)(nil)
	_ proto.Message = (*Tstat)(nil)
	_ proto.Message = (*Rstat)(nil)
	_ proto.Message = (*Twstat)(nil)
	_ proto.Message = (*Rwstat)(nil)
)
