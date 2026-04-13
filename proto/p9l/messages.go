package p9l

import (
	"fmt"
	"io"

	"github.com/dotwaffle/ninep/proto"
)

// Rlerror is the server's error response, carrying a Linux errno value.
// Wire body: ecode[4].
type Rlerror struct {
	Ecode proto.Errno
}

// Type returns proto.TypeRlerror.
func (m *Rlerror) Type() proto.MessageType { return proto.TypeRlerror }

// EncodeTo writes the Rlerror body: ecode[4].
func (m *Rlerror) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Ecode)); err != nil {
		return fmt.Errorf("encode rlerror ecode: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rlerror body: ecode[4].
func (m *Rlerror) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode rlerror ecode: %w", err)
	}
	m.Ecode = proto.Errno(v)
	return nil
}

// Tstatfs requests filesystem statistics for the file referenced by Fid.
// Wire body: fid[4].
type Tstatfs struct {
	Fid proto.Fid
}

// Type returns proto.TypeTstatfs.
func (m *Tstatfs) Type() proto.MessageType { return proto.TypeTstatfs }

// EncodeTo writes the Tstatfs body: fid[4].
func (m *Tstatfs) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tstatfs fid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tstatfs body: fid[4].
func (m *Tstatfs) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tstatfs fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	return nil
}

// Rstatfs carries filesystem statistics in response to Tstatfs.
// Wire body: type[4] bsize[4] blocks[8] bfree[8] bavail[8] files[8] ffree[8] fsid[8] namelen[4].
type Rstatfs struct {
	Stat proto.FSStat
}

// Type returns proto.TypeRstatfs.
func (m *Rstatfs) Type() proto.MessageType { return proto.TypeRstatfs }

// EncodeTo writes the Rstatfs body: all 9 FSStat fields in wire order.
func (m *Rstatfs) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, m.Stat.Type); err != nil {
		return fmt.Errorf("encode rstatfs type: %w", err)
	}
	if err := proto.WriteUint32(w, m.Stat.BSize); err != nil {
		return fmt.Errorf("encode rstatfs bsize: %w", err)
	}
	if err := proto.WriteUint64(w, m.Stat.Blocks); err != nil {
		return fmt.Errorf("encode rstatfs blocks: %w", err)
	}
	if err := proto.WriteUint64(w, m.Stat.BFree); err != nil {
		return fmt.Errorf("encode rstatfs bfree: %w", err)
	}
	if err := proto.WriteUint64(w, m.Stat.BAvail); err != nil {
		return fmt.Errorf("encode rstatfs bavail: %w", err)
	}
	if err := proto.WriteUint64(w, m.Stat.Files); err != nil {
		return fmt.Errorf("encode rstatfs files: %w", err)
	}
	if err := proto.WriteUint64(w, m.Stat.FFree); err != nil {
		return fmt.Errorf("encode rstatfs ffree: %w", err)
	}
	if err := proto.WriteUint64(w, m.Stat.FSID); err != nil {
		return fmt.Errorf("encode rstatfs fsid: %w", err)
	}
	if err := proto.WriteUint32(w, m.Stat.NameLen); err != nil {
		return fmt.Errorf("encode rstatfs namelen: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rstatfs body: all 9 FSStat fields in wire order.
func (m *Rstatfs) DecodeFrom(r io.Reader) error {
	var err error
	if m.Stat.Type, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rstatfs type: %w", err)
	}
	if m.Stat.BSize, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rstatfs bsize: %w", err)
	}
	if m.Stat.Blocks, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rstatfs blocks: %w", err)
	}
	if m.Stat.BFree, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rstatfs bfree: %w", err)
	}
	if m.Stat.BAvail, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rstatfs bavail: %w", err)
	}
	if m.Stat.Files, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rstatfs files: %w", err)
	}
	if m.Stat.FFree, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rstatfs ffree: %w", err)
	}
	if m.Stat.FSID, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rstatfs fsid: %w", err)
	}
	if m.Stat.NameLen, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rstatfs namelen: %w", err)
	}
	return nil
}

// Tlopen requests opening the file referenced by Fid with the given flags.
// Wire body: fid[4] flags[4].
type Tlopen struct {
	Fid   proto.Fid
	Flags uint32
}

// Type returns proto.TypeTlopen.
func (m *Tlopen) Type() proto.MessageType { return proto.TypeTlopen }

// EncodeTo writes the Tlopen body: fid[4] flags[4].
func (m *Tlopen) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tlopen fid: %w", err)
	}
	if err := proto.WriteUint32(w, m.Flags); err != nil {
		return fmt.Errorf("encode tlopen flags: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tlopen body: fid[4] flags[4].
func (m *Tlopen) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlopen fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	if m.Flags, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tlopen flags: %w", err)
	}
	return nil
}

// Rlopen is the server's response to Tlopen, providing the opened file's QID
// and the negotiated I/O unit size.
// Wire body: qid[13] iounit[4].
type Rlopen struct {
	QID    proto.QID
	IOUnit uint32
}

// Type returns proto.TypeRlopen.
func (m *Rlopen) Type() proto.MessageType { return proto.TypeRlopen }

// EncodeTo writes the Rlopen body: qid[13] iounit[4].
func (m *Rlopen) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rlopen qid: %w", err)
	}
	if err := proto.WriteUint32(w, m.IOUnit); err != nil {
		return fmt.Errorf("encode rlopen iounit: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rlopen body: qid[13] iounit[4].
func (m *Rlopen) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rlopen qid: %w", err)
	}
	if m.IOUnit, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rlopen iounit: %w", err)
	}
	return nil
}

// Tlcreate requests creating and opening a file in the directory referenced
// by Fid.
// Wire body: fid[4] name[s] flags[4] mode[4] gid[4].
type Tlcreate struct {
	Fid   proto.Fid
	Name  string
	Flags uint32
	Mode  proto.FileMode
	GID   uint32
}

// Type returns proto.TypeTlcreate.
func (m *Tlcreate) Type() proto.MessageType { return proto.TypeTlcreate }

// EncodeTo writes the Tlcreate body: fid[4] name[s] flags[4] mode[4] gid[4].
func (m *Tlcreate) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tlcreate fid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tlcreate name: %w", err)
	}
	if err := proto.WriteUint32(w, m.Flags); err != nil {
		return fmt.Errorf("encode tlcreate flags: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Mode)); err != nil {
		return fmt.Errorf("encode tlcreate mode: %w", err)
	}
	if err := proto.WriteUint32(w, m.GID); err != nil {
		return fmt.Errorf("encode tlcreate gid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tlcreate body: fid[4] name[s] flags[4] mode[4] gid[4].
func (m *Tlcreate) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlcreate fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tlcreate name: %w", err)
	}
	if m.Flags, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tlcreate flags: %w", err)
	}
	mode, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlcreate mode: %w", err)
	}
	m.Mode = proto.FileMode(mode)
	if m.GID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tlcreate gid: %w", err)
	}
	return nil
}

// Rlcreate is the server's response to Tlcreate, providing the new file's
// QID and I/O unit size.
// Wire body: qid[13] iounit[4].
type Rlcreate struct {
	QID    proto.QID
	IOUnit uint32
}

// Type returns proto.TypeRlcreate.
func (m *Rlcreate) Type() proto.MessageType { return proto.TypeRlcreate }

// EncodeTo writes the Rlcreate body: qid[13] iounit[4].
func (m *Rlcreate) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rlcreate qid: %w", err)
	}
	if err := proto.WriteUint32(w, m.IOUnit); err != nil {
		return fmt.Errorf("encode rlcreate iounit: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rlcreate body: qid[13] iounit[4].
func (m *Rlcreate) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rlcreate qid: %w", err)
	}
	if m.IOUnit, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rlcreate iounit: %w", err)
	}
	return nil
}

// Tsymlink requests creating a symbolic link in the directory referenced by
// DirFid.
// Wire body: fid[4] name[s] symtgt[s] gid[4].
type Tsymlink struct {
	DirFid proto.Fid
	Name   string
	Target string
	GID    uint32
}

// Type returns proto.TypeTsymlink.
func (m *Tsymlink) Type() proto.MessageType { return proto.TypeTsymlink }

// EncodeTo writes the Tsymlink body: fid[4] name[s] symtgt[s] gid[4].
func (m *Tsymlink) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.DirFid)); err != nil {
		return fmt.Errorf("encode tsymlink dirfid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tsymlink name: %w", err)
	}
	if err := proto.WriteString(w, m.Target); err != nil {
		return fmt.Errorf("encode tsymlink target: %w", err)
	}
	if err := proto.WriteUint32(w, m.GID); err != nil {
		return fmt.Errorf("encode tsymlink gid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tsymlink body: fid[4] name[s] symtgt[s] gid[4].
func (m *Tsymlink) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tsymlink dirfid: %w", err)
	}
	m.DirFid = proto.Fid(v)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tsymlink name: %w", err)
	}
	if m.Target, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tsymlink target: %w", err)
	}
	if m.GID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tsymlink gid: %w", err)
	}
	return nil
}

// Rsymlink is the server's response to Tsymlink, providing the new symlink's
// QID.
// Wire body: qid[13].
type Rsymlink struct {
	QID proto.QID
}

// Type returns proto.TypeRsymlink.
func (m *Rsymlink) Type() proto.MessageType { return proto.TypeRsymlink }

// EncodeTo writes the Rsymlink body: qid[13].
func (m *Rsymlink) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rsymlink qid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rsymlink body: qid[13].
func (m *Rsymlink) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rsymlink qid: %w", err)
	}
	return nil
}

// Tmknod requests creating a device node in the directory referenced by DirFid.
// Wire body: dfid[4] name[s] mode[4] major[4] minor[4] gid[4].
type Tmknod struct {
	DirFid proto.Fid
	Name   string
	Mode   proto.FileMode
	Major  uint32
	Minor  uint32
	GID    uint32
}

// Type returns proto.TypeTmknod.
func (m *Tmknod) Type() proto.MessageType { return proto.TypeTmknod }

// EncodeTo writes the Tmknod body: dfid[4] name[s] mode[4] major[4] minor[4] gid[4].
func (m *Tmknod) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.DirFid)); err != nil {
		return fmt.Errorf("encode tmknod dirfid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tmknod name: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Mode)); err != nil {
		return fmt.Errorf("encode tmknod mode: %w", err)
	}
	if err := proto.WriteUint32(w, m.Major); err != nil {
		return fmt.Errorf("encode tmknod major: %w", err)
	}
	if err := proto.WriteUint32(w, m.Minor); err != nil {
		return fmt.Errorf("encode tmknod minor: %w", err)
	}
	if err := proto.WriteUint32(w, m.GID); err != nil {
		return fmt.Errorf("encode tmknod gid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tmknod body: dfid[4] name[s] mode[4] major[4] minor[4] gid[4].
func (m *Tmknod) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tmknod dirfid: %w", err)
	}
	m.DirFid = proto.Fid(v)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tmknod name: %w", err)
	}
	mode, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tmknod mode: %w", err)
	}
	m.Mode = proto.FileMode(mode)
	if m.Major, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tmknod major: %w", err)
	}
	if m.Minor, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tmknod minor: %w", err)
	}
	if m.GID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tmknod gid: %w", err)
	}
	return nil
}

// Rmknod is the server's response to Tmknod, providing the new node's QID.
// Wire body: qid[13].
type Rmknod struct {
	QID proto.QID
}

// Type returns proto.TypeRmknod.
func (m *Rmknod) Type() proto.MessageType { return proto.TypeRmknod }

// EncodeTo writes the Rmknod body: qid[13].
func (m *Rmknod) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rmknod qid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rmknod body: qid[13].
func (m *Rmknod) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rmknod qid: %w", err)
	}
	return nil
}

// Trename requests renaming the file referenced by Fid into the directory
// referenced by DirFid with the new Name.
// Wire body: fid[4] dfid[4] name[s].
type Trename struct {
	Fid    proto.Fid
	DirFid proto.Fid
	Name   string
}

// Type returns proto.TypeTrename.
func (m *Trename) Type() proto.MessageType { return proto.TypeTrename }

// EncodeTo writes the Trename body: fid[4] dfid[4] name[s].
func (m *Trename) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode trename fid: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.DirFid)); err != nil {
		return fmt.Errorf("encode trename dirfid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode trename name: %w", err)
	}
	return nil
}

// DecodeFrom reads the Trename body: fid[4] dfid[4] name[s].
func (m *Trename) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode trename fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	dfid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode trename dirfid: %w", err)
	}
	m.DirFid = proto.Fid(dfid)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode trename name: %w", err)
	}
	return nil
}

// Rrename confirms a rename operation. It has an empty body.
type Rrename struct{}

// Type returns proto.TypeRrename.
func (m *Rrename) Type() proto.MessageType { return proto.TypeRrename }

// EncodeTo writes nothing; Rrename has an empty body.
func (m *Rrename) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rrename has an empty body.
func (m *Rrename) DecodeFrom(_ io.Reader) error { return nil }

// Treadlink requests reading the target of the symbolic link referenced by Fid.
// Wire body: fid[4].
type Treadlink struct {
	Fid proto.Fid
}

// Type returns proto.TypeTreadlink.
func (m *Treadlink) Type() proto.MessageType { return proto.TypeTreadlink }

// EncodeTo writes the Treadlink body: fid[4].
func (m *Treadlink) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode treadlink fid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Treadlink body: fid[4].
func (m *Treadlink) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode treadlink fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	return nil
}

// Rreadlink carries the target path of a symbolic link.
// Wire body: target[s].
type Rreadlink struct {
	Target string
}

// Type returns proto.TypeRreadlink.
func (m *Rreadlink) Type() proto.MessageType { return proto.TypeRreadlink }

// EncodeTo writes the Rreadlink body: target[s].
func (m *Rreadlink) EncodeTo(w io.Writer) error {
	if err := proto.WriteString(w, m.Target); err != nil {
		return fmt.Errorf("encode rreadlink target: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rreadlink body: target[s].
func (m *Rreadlink) DecodeFrom(r io.Reader) error {
	var err error
	if m.Target, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode rreadlink target: %w", err)
	}
	return nil
}

// Tgetattr requests file attributes for the file referenced by Fid.
// The RequestMask selects which attributes to return.
// Wire body: fid[4] request_mask[8].
type Tgetattr struct {
	Fid         proto.Fid
	RequestMask proto.AttrMask
}

// Type returns proto.TypeTgetattr.
func (m *Tgetattr) Type() proto.MessageType { return proto.TypeTgetattr }

// EncodeTo writes the Tgetattr body: fid[4] request_mask[8].
func (m *Tgetattr) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tgetattr fid: %w", err)
	}
	if err := proto.WriteUint64(w, uint64(m.RequestMask)); err != nil {
		return fmt.Errorf("encode tgetattr request_mask: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tgetattr body: fid[4] request_mask[8].
func (m *Tgetattr) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tgetattr fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	mask, err := proto.ReadUint64(r)
	if err != nil {
		return fmt.Errorf("decode tgetattr request_mask: %w", err)
	}
	m.RequestMask = proto.AttrMask(mask)
	return nil
}

// Rgetattr carries file attributes in response to Tgetattr. The body contains
// all 20 Attr fields (160 bytes) in fixed wire order: valid[8] qid[13] mode[4]
// uid[4] gid[4] nlink[8] rdev[8] size[8] blksize[8] blocks[8] atime_sec[8]
// atime_nsec[8] mtime_sec[8] mtime_nsec[8] ctime_sec[8] ctime_nsec[8]
// btime_sec[8] btime_nsec[8] gen[8] data_version[8].
type Rgetattr struct {
	Attr proto.Attr
}

// Type returns proto.TypeRgetattr.
func (m *Rgetattr) Type() proto.MessageType { return proto.TypeRgetattr }

// EncodeTo writes all 20 Attr fields in wire order.
func (m *Rgetattr) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint64(w, uint64(m.Attr.Valid)); err != nil {
		return fmt.Errorf("encode rgetattr valid: %w", err)
	}
	if err := proto.WriteQID(w, m.Attr.QID); err != nil {
		return fmt.Errorf("encode rgetattr qid: %w", err)
	}
	if err := proto.WriteUint32(w, m.Attr.Mode); err != nil {
		return fmt.Errorf("encode rgetattr mode: %w", err)
	}
	if err := proto.WriteUint32(w, m.Attr.UID); err != nil {
		return fmt.Errorf("encode rgetattr uid: %w", err)
	}
	if err := proto.WriteUint32(w, m.Attr.GID); err != nil {
		return fmt.Errorf("encode rgetattr gid: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.NLink); err != nil {
		return fmt.Errorf("encode rgetattr nlink: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.RDev); err != nil {
		return fmt.Errorf("encode rgetattr rdev: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.Size); err != nil {
		return fmt.Errorf("encode rgetattr size: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.BlkSize); err != nil {
		return fmt.Errorf("encode rgetattr blksize: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.Blocks); err != nil {
		return fmt.Errorf("encode rgetattr blocks: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.ATimeSec); err != nil {
		return fmt.Errorf("encode rgetattr atime_sec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.ATimeNSec); err != nil {
		return fmt.Errorf("encode rgetattr atime_nsec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.MTimeSec); err != nil {
		return fmt.Errorf("encode rgetattr mtime_sec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.MTimeNSec); err != nil {
		return fmt.Errorf("encode rgetattr mtime_nsec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.CTimeSec); err != nil {
		return fmt.Errorf("encode rgetattr ctime_sec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.CTimeNSec); err != nil {
		return fmt.Errorf("encode rgetattr ctime_nsec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.BTimeSec); err != nil {
		return fmt.Errorf("encode rgetattr btime_sec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.BTimeNSec); err != nil {
		return fmt.Errorf("encode rgetattr btime_nsec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.Gen); err != nil {
		return fmt.Errorf("encode rgetattr gen: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.DataVersion); err != nil {
		return fmt.Errorf("encode rgetattr data_version: %w", err)
	}
	return nil
}

// DecodeFrom reads all 20 Attr fields in wire order.
func (m *Rgetattr) DecodeFrom(r io.Reader) error {
	valid, err := proto.ReadUint64(r)
	if err != nil {
		return fmt.Errorf("decode rgetattr valid: %w", err)
	}
	m.Attr.Valid = proto.AttrMask(valid)
	if m.Attr.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rgetattr qid: %w", err)
	}
	if m.Attr.Mode, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rgetattr mode: %w", err)
	}
	if m.Attr.UID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rgetattr uid: %w", err)
	}
	if m.Attr.GID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rgetattr gid: %w", err)
	}
	if m.Attr.NLink, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr nlink: %w", err)
	}
	if m.Attr.RDev, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr rdev: %w", err)
	}
	if m.Attr.Size, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr size: %w", err)
	}
	if m.Attr.BlkSize, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr blksize: %w", err)
	}
	if m.Attr.Blocks, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr blocks: %w", err)
	}
	if m.Attr.ATimeSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr atime_sec: %w", err)
	}
	if m.Attr.ATimeNSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr atime_nsec: %w", err)
	}
	if m.Attr.MTimeSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr mtime_sec: %w", err)
	}
	if m.Attr.MTimeNSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr mtime_nsec: %w", err)
	}
	if m.Attr.CTimeSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr ctime_sec: %w", err)
	}
	if m.Attr.CTimeNSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr ctime_nsec: %w", err)
	}
	if m.Attr.BTimeSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr btime_sec: %w", err)
	}
	if m.Attr.BTimeNSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr btime_nsec: %w", err)
	}
	if m.Attr.Gen, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr gen: %w", err)
	}
	if m.Attr.DataVersion, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetattr data_version: %w", err)
	}
	return nil
}

// Tsetattr requests setting file attributes on the file referenced by Fid.
// Wire body: fid[4] valid[4] mode[4] uid[4] gid[4] size[8] atime_sec[8]
// atime_nsec[8] mtime_sec[8] mtime_nsec[8].
type Tsetattr struct {
	Fid  proto.Fid
	Attr proto.SetAttr
}

// Type returns proto.TypeTsetattr.
func (m *Tsetattr) Type() proto.MessageType { return proto.TypeTsetattr }

// EncodeTo writes the Tsetattr body: fid[4] then all 9 SetAttr fields.
func (m *Tsetattr) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tsetattr fid: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Attr.Valid)); err != nil {
		return fmt.Errorf("encode tsetattr valid: %w", err)
	}
	if err := proto.WriteUint32(w, m.Attr.Mode); err != nil {
		return fmt.Errorf("encode tsetattr mode: %w", err)
	}
	if err := proto.WriteUint32(w, m.Attr.UID); err != nil {
		return fmt.Errorf("encode tsetattr uid: %w", err)
	}
	if err := proto.WriteUint32(w, m.Attr.GID); err != nil {
		return fmt.Errorf("encode tsetattr gid: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.Size); err != nil {
		return fmt.Errorf("encode tsetattr size: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.ATimeSec); err != nil {
		return fmt.Errorf("encode tsetattr atime_sec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.ATimeNSec); err != nil {
		return fmt.Errorf("encode tsetattr atime_nsec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.MTimeSec); err != nil {
		return fmt.Errorf("encode tsetattr mtime_sec: %w", err)
	}
	if err := proto.WriteUint64(w, m.Attr.MTimeNSec); err != nil {
		return fmt.Errorf("encode tsetattr mtime_nsec: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tsetattr body: fid[4] then all 9 SetAttr fields.
func (m *Tsetattr) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tsetattr fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	valid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tsetattr valid: %w", err)
	}
	m.Attr.Valid = proto.SetAttrMask(valid)
	if m.Attr.Mode, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tsetattr mode: %w", err)
	}
	if m.Attr.UID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tsetattr uid: %w", err)
	}
	if m.Attr.GID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tsetattr gid: %w", err)
	}
	if m.Attr.Size, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tsetattr size: %w", err)
	}
	if m.Attr.ATimeSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tsetattr atime_sec: %w", err)
	}
	if m.Attr.ATimeNSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tsetattr atime_nsec: %w", err)
	}
	if m.Attr.MTimeSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tsetattr mtime_sec: %w", err)
	}
	if m.Attr.MTimeNSec, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tsetattr mtime_nsec: %w", err)
	}
	return nil
}

// Rsetattr confirms a setattr operation. It has an empty body.
type Rsetattr struct{}

// Type returns proto.TypeRsetattr.
func (m *Rsetattr) Type() proto.MessageType { return proto.TypeRsetattr }

// EncodeTo writes nothing; Rsetattr has an empty body.
func (m *Rsetattr) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rsetattr has an empty body.
func (m *Rsetattr) DecodeFrom(_ io.Reader) error { return nil }

// Txattrwalk requests walking to the extended attribute named Name on the
// file referenced by Fid, associating NewFid with the xattr.
// Wire body: fid[4] newfid[4] name[s].
type Txattrwalk struct {
	Fid    proto.Fid
	NewFid proto.Fid
	Name   string
}

// Type returns proto.TypeTxattrwalk.
func (m *Txattrwalk) Type() proto.MessageType { return proto.TypeTxattrwalk }

// EncodeTo writes the Txattrwalk body: fid[4] newfid[4] name[s].
func (m *Txattrwalk) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode txattrwalk fid: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.NewFid)); err != nil {
		return fmt.Errorf("encode txattrwalk newfid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode txattrwalk name: %w", err)
	}
	return nil
}

// DecodeFrom reads the Txattrwalk body: fid[4] newfid[4] name[s].
func (m *Txattrwalk) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode txattrwalk fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	nf, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode txattrwalk newfid: %w", err)
	}
	m.NewFid = proto.Fid(nf)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode txattrwalk name: %w", err)
	}
	return nil
}

// Rxattrwalk is the server's response to Txattrwalk, reporting the total size
// of the extended attribute value.
// Wire body: size[8].
type Rxattrwalk struct {
	Size uint64
}

// Type returns proto.TypeRxattrwalk.
func (m *Rxattrwalk) Type() proto.MessageType { return proto.TypeRxattrwalk }

// EncodeTo writes the Rxattrwalk body: size[8].
func (m *Rxattrwalk) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint64(w, m.Size); err != nil {
		return fmt.Errorf("encode rxattrwalk size: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rxattrwalk body: size[8].
func (m *Rxattrwalk) DecodeFrom(r io.Reader) error {
	var err error
	if m.Size, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rxattrwalk size: %w", err)
	}
	return nil
}

// Txattrcreate requests creating an extended attribute on the file referenced
// by Fid.
// Wire body: fid[4] name[s] attr_size[8] flags[4].
type Txattrcreate struct {
	Fid      proto.Fid
	Name     string
	AttrSize uint64
	Flags    uint32
}

// Type returns proto.TypeTxattrcreate.
func (m *Txattrcreate) Type() proto.MessageType { return proto.TypeTxattrcreate }

// EncodeTo writes the Txattrcreate body: fid[4] name[s] attr_size[8] flags[4].
func (m *Txattrcreate) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode txattrcreate fid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode txattrcreate name: %w", err)
	}
	if err := proto.WriteUint64(w, m.AttrSize); err != nil {
		return fmt.Errorf("encode txattrcreate attr_size: %w", err)
	}
	if err := proto.WriteUint32(w, m.Flags); err != nil {
		return fmt.Errorf("encode txattrcreate flags: %w", err)
	}
	return nil
}

// DecodeFrom reads the Txattrcreate body: fid[4] name[s] attr_size[8] flags[4].
func (m *Txattrcreate) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode txattrcreate fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode txattrcreate name: %w", err)
	}
	if m.AttrSize, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode txattrcreate attr_size: %w", err)
	}
	if m.Flags, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode txattrcreate flags: %w", err)
	}
	return nil
}

// Rxattrcreate confirms an xattr create operation. It has an empty body.
type Rxattrcreate struct{}

// Type returns proto.TypeRxattrcreate.
func (m *Rxattrcreate) Type() proto.MessageType { return proto.TypeRxattrcreate }

// EncodeTo writes nothing; Rxattrcreate has an empty body.
func (m *Rxattrcreate) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rxattrcreate has an empty body.
func (m *Rxattrcreate) DecodeFrom(_ io.Reader) error { return nil }

// Treaddir requests reading directory entries from the directory referenced
// by Fid, starting at the given Offset.
// Wire body: fid[4] offset[8] count[4].
type Treaddir struct {
	Fid    proto.Fid
	Offset uint64
	Count  uint32
}

// Type returns proto.TypeTreaddir.
func (m *Treaddir) Type() proto.MessageType { return proto.TypeTreaddir }

// EncodeTo writes the Treaddir body: fid[4] offset[8] count[4].
func (m *Treaddir) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode treaddir fid: %w", err)
	}
	if err := proto.WriteUint64(w, m.Offset); err != nil {
		return fmt.Errorf("encode treaddir offset: %w", err)
	}
	if err := proto.WriteUint32(w, m.Count); err != nil {
		return fmt.Errorf("encode treaddir count: %w", err)
	}
	return nil
}

// DecodeFrom reads the Treaddir body: fid[4] offset[8] count[4].
func (m *Treaddir) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode treaddir fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	if m.Offset, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode treaddir offset: %w", err)
	}
	if m.Count, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode treaddir count: %w", err)
	}
	return nil
}

// Rreaddir carries raw directory entry data in response to Treaddir.
// Wire body: count[4] data[count].
type Rreaddir struct {
	Data []byte
}

// Type returns proto.TypeRreaddir.
func (m *Rreaddir) Type() proto.MessageType { return proto.TypeRreaddir }

// EncodeTo writes the Rreaddir body: count[4] data[count].
func (m *Rreaddir) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(len(m.Data))); err != nil {
		return fmt.Errorf("encode rreaddir count: %w", err)
	}
	if len(m.Data) > 0 {
		if _, err := w.Write(m.Data); err != nil {
			return fmt.Errorf("encode rreaddir data: %w", err)
		}
	}
	return nil
}

// DecodeFrom reads the Rreaddir body: count[4] data[count].
func (m *Rreaddir) DecodeFrom(r io.Reader) error {
	count, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode rreaddir count: %w", err)
	}
	if count > proto.MaxDataSize {
		return fmt.Errorf("decode rreaddir count %d exceeds maximum %d", count, proto.MaxDataSize)
	}
	m.Data = make([]byte, count)
	if count > 0 {
		if _, err := io.ReadFull(r, m.Data); err != nil {
			return fmt.Errorf("decode rreaddir data: %w", err)
		}
	}
	return nil
}

// Tfsync requests syncing the file referenced by Fid to storage.
// Wire body: fid[4] datasync[4].
type Tfsync struct {
	Fid      proto.Fid
	DataSync uint32
}

// Type returns proto.TypeTfsync.
func (m *Tfsync) Type() proto.MessageType { return proto.TypeTfsync }

// EncodeTo writes the Tfsync body: fid[4] datasync[4].
func (m *Tfsync) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tfsync fid: %w", err)
	}
	if err := proto.WriteUint32(w, m.DataSync); err != nil {
		return fmt.Errorf("encode tfsync datasync: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tfsync body: fid[4] datasync[4].
func (m *Tfsync) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tfsync fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	if m.DataSync, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tfsync datasync: %w", err)
	}
	return nil
}

// Rfsync confirms an fsync operation. It has an empty body.
type Rfsync struct{}

// Type returns proto.TypeRfsync.
func (m *Rfsync) Type() proto.MessageType { return proto.TypeRfsync }

// EncodeTo writes nothing; Rfsync has an empty body.
func (m *Rfsync) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rfsync has an empty body.
func (m *Rfsync) DecodeFrom(_ io.Reader) error { return nil }

// Tlock requests acquiring or releasing a POSIX byte-range lock on the file
// referenced by Fid.
// Wire body: fid[4] type[1] flags[4] start[8] length[8] proc_id[4] client_id[s].
type Tlock struct {
	Fid      proto.Fid
	LockType proto.LockType
	Flags    proto.LockFlags
	Start    uint64
	Length   uint64
	ProcID   uint32
	ClientID string
}

// Type returns proto.TypeTlock.
func (m *Tlock) Type() proto.MessageType { return proto.TypeTlock }

// EncodeTo writes the Tlock body.
func (m *Tlock) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tlock fid: %w", err)
	}
	if err := proto.WriteUint8(w, uint8(m.LockType)); err != nil {
		return fmt.Errorf("encode tlock type: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Flags)); err != nil {
		return fmt.Errorf("encode tlock flags: %w", err)
	}
	if err := proto.WriteUint64(w, m.Start); err != nil {
		return fmt.Errorf("encode tlock start: %w", err)
	}
	if err := proto.WriteUint64(w, m.Length); err != nil {
		return fmt.Errorf("encode tlock length: %w", err)
	}
	if err := proto.WriteUint32(w, m.ProcID); err != nil {
		return fmt.Errorf("encode tlock proc_id: %w", err)
	}
	if err := proto.WriteString(w, m.ClientID); err != nil {
		return fmt.Errorf("encode tlock client_id: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tlock body.
func (m *Tlock) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlock fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	lt, err := proto.ReadUint8(r)
	if err != nil {
		return fmt.Errorf("decode tlock type: %w", err)
	}
	if lt > uint8(proto.LockTypeUnlck) {
		return fmt.Errorf("decode tlock type: invalid lock type %d", lt)
	}
	m.LockType = proto.LockType(lt)
	flags, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlock flags: %w", err)
	}
	if proto.LockFlags(flags)&^(proto.LockFlagBlock|proto.LockFlagReclaim) != 0 {
		return fmt.Errorf("decode tlock flags: invalid flags %#x", flags)
	}
	m.Flags = proto.LockFlags(flags)
	if m.Start, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tlock start: %w", err)
	}
	if m.Length, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tlock length: %w", err)
	}
	if m.ProcID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tlock proc_id: %w", err)
	}
	if m.ClientID, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tlock client_id: %w", err)
	}
	return nil
}

// Rlock is the server's response to Tlock, reporting the lock status.
// Wire body: status[1].
type Rlock struct {
	Status proto.LockStatus
}

// Type returns proto.TypeRlock.
func (m *Rlock) Type() proto.MessageType { return proto.TypeRlock }

// EncodeTo writes the Rlock body: status[1].
func (m *Rlock) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint8(w, uint8(m.Status)); err != nil {
		return fmt.Errorf("encode rlock status: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rlock body: status[1].
func (m *Rlock) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint8(r)
	if err != nil {
		return fmt.Errorf("decode rlock status: %w", err)
	}
	if v > uint8(proto.LockStatusGrace) {
		return fmt.Errorf("decode rlock status: invalid lock status %d", v)
	}
	m.Status = proto.LockStatus(v)
	return nil
}

// Tgetlock requests testing whether a POSIX byte-range lock could be placed
// on the file referenced by Fid.
// Wire body: fid[4] type[1] start[8] length[8] proc_id[4] client_id[s].
type Tgetlock struct {
	Fid      proto.Fid
	LockType proto.LockType
	Start    uint64
	Length   uint64
	ProcID   uint32
	ClientID string
}

// Type returns proto.TypeTgetlock.
func (m *Tgetlock) Type() proto.MessageType { return proto.TypeTgetlock }

// EncodeTo writes the Tgetlock body.
func (m *Tgetlock) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tgetlock fid: %w", err)
	}
	if err := proto.WriteUint8(w, uint8(m.LockType)); err != nil {
		return fmt.Errorf("encode tgetlock type: %w", err)
	}
	if err := proto.WriteUint64(w, m.Start); err != nil {
		return fmt.Errorf("encode tgetlock start: %w", err)
	}
	if err := proto.WriteUint64(w, m.Length); err != nil {
		return fmt.Errorf("encode tgetlock length: %w", err)
	}
	if err := proto.WriteUint32(w, m.ProcID); err != nil {
		return fmt.Errorf("encode tgetlock proc_id: %w", err)
	}
	if err := proto.WriteString(w, m.ClientID); err != nil {
		return fmt.Errorf("encode tgetlock client_id: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tgetlock body.
func (m *Tgetlock) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tgetlock fid: %w", err)
	}
	m.Fid = proto.Fid(v)
	lt, err := proto.ReadUint8(r)
	if err != nil {
		return fmt.Errorf("decode tgetlock type: %w", err)
	}
	if lt > uint8(proto.LockTypeUnlck) {
		return fmt.Errorf("decode tgetlock type: invalid lock type %d", lt)
	}
	m.LockType = proto.LockType(lt)
	if m.Start, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tgetlock start: %w", err)
	}
	if m.Length, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode tgetlock length: %w", err)
	}
	if m.ProcID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tgetlock proc_id: %w", err)
	}
	if m.ClientID, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tgetlock client_id: %w", err)
	}
	return nil
}

// Rgetlock is the server's response to Tgetlock, describing the conflicting
// lock (if any) or the requested lock parameters.
// Wire body: type[1] start[8] length[8] proc_id[4] client_id[s].
type Rgetlock struct {
	LockType proto.LockType
	Start    uint64
	Length   uint64
	ProcID   uint32
	ClientID string
}

// Type returns proto.TypeRgetlock.
func (m *Rgetlock) Type() proto.MessageType { return proto.TypeRgetlock }

// EncodeTo writes the Rgetlock body.
func (m *Rgetlock) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint8(w, uint8(m.LockType)); err != nil {
		return fmt.Errorf("encode rgetlock type: %w", err)
	}
	if err := proto.WriteUint64(w, m.Start); err != nil {
		return fmt.Errorf("encode rgetlock start: %w", err)
	}
	if err := proto.WriteUint64(w, m.Length); err != nil {
		return fmt.Errorf("encode rgetlock length: %w", err)
	}
	if err := proto.WriteUint32(w, m.ProcID); err != nil {
		return fmt.Errorf("encode rgetlock proc_id: %w", err)
	}
	if err := proto.WriteString(w, m.ClientID); err != nil {
		return fmt.Errorf("encode rgetlock client_id: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rgetlock body.
func (m *Rgetlock) DecodeFrom(r io.Reader) error {
	lt, err := proto.ReadUint8(r)
	if err != nil {
		return fmt.Errorf("decode rgetlock type: %w", err)
	}
	if lt > uint8(proto.LockTypeUnlck) {
		return fmt.Errorf("decode rgetlock type: invalid lock type %d", lt)
	}
	m.LockType = proto.LockType(lt)
	if m.Start, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetlock start: %w", err)
	}
	if m.Length, err = proto.ReadUint64(r); err != nil {
		return fmt.Errorf("decode rgetlock length: %w", err)
	}
	if m.ProcID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode rgetlock proc_id: %w", err)
	}
	if m.ClientID, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode rgetlock client_id: %w", err)
	}
	return nil
}

// Tlink requests creating a hard link in the directory referenced by DirFid
// to the file referenced by Fid.
// Wire body: dfid[4] fid[4] name[s].
type Tlink struct {
	DirFid proto.Fid
	Fid    proto.Fid
	Name   string
}

// Type returns proto.TypeTlink.
func (m *Tlink) Type() proto.MessageType { return proto.TypeTlink }

// EncodeTo writes the Tlink body: dfid[4] fid[4] name[s].
func (m *Tlink) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.DirFid)); err != nil {
		return fmt.Errorf("encode tlink dirfid: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tlink fid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tlink name: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tlink body: dfid[4] fid[4] name[s].
func (m *Tlink) DecodeFrom(r io.Reader) error {
	dfid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlink dirfid: %w", err)
	}
	m.DirFid = proto.Fid(dfid)
	fid, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tlink fid: %w", err)
	}
	m.Fid = proto.Fid(fid)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tlink name: %w", err)
	}
	return nil
}

// Rlink confirms a hard link operation. It has an empty body.
type Rlink struct{}

// Type returns proto.TypeRlink.
func (m *Rlink) Type() proto.MessageType { return proto.TypeRlink }

// EncodeTo writes nothing; Rlink has an empty body.
func (m *Rlink) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rlink has an empty body.
func (m *Rlink) DecodeFrom(_ io.Reader) error { return nil }

// Tmkdir requests creating a directory in the directory referenced by DirFid.
// Wire body: dfid[4] name[s] mode[4] gid[4].
type Tmkdir struct {
	DirFid proto.Fid
	Name   string
	Mode   proto.FileMode
	GID    uint32
}

// Type returns proto.TypeTmkdir.
func (m *Tmkdir) Type() proto.MessageType { return proto.TypeTmkdir }

// EncodeTo writes the Tmkdir body: dfid[4] name[s] mode[4] gid[4].
func (m *Tmkdir) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.DirFid)); err != nil {
		return fmt.Errorf("encode tmkdir dirfid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tmkdir name: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.Mode)); err != nil {
		return fmt.Errorf("encode tmkdir mode: %w", err)
	}
	if err := proto.WriteUint32(w, m.GID); err != nil {
		return fmt.Errorf("encode tmkdir gid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tmkdir body: dfid[4] name[s] mode[4] gid[4].
func (m *Tmkdir) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tmkdir dirfid: %w", err)
	}
	m.DirFid = proto.Fid(v)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tmkdir name: %w", err)
	}
	mode, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tmkdir mode: %w", err)
	}
	m.Mode = proto.FileMode(mode)
	if m.GID, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tmkdir gid: %w", err)
	}
	return nil
}

// Rmkdir is the server's response to Tmkdir, providing the new directory's QID.
// Wire body: qid[13].
type Rmkdir struct {
	QID proto.QID
}

// Type returns proto.TypeRmkdir.
func (m *Rmkdir) Type() proto.MessageType { return proto.TypeRmkdir }

// EncodeTo writes the Rmkdir body: qid[13].
func (m *Rmkdir) EncodeTo(w io.Writer) error {
	if err := proto.WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rmkdir qid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rmkdir body: qid[13].
func (m *Rmkdir) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = proto.ReadQID(r); err != nil {
		return fmt.Errorf("decode rmkdir qid: %w", err)
	}
	return nil
}

// Trenameat requests renaming a file from one directory to another using
// directory fids and names (AT-style).
// Wire body: olddirfid[4] oldname[s] newdirfid[4] newname[s].
type Trenameat struct {
	OldDirFid proto.Fid
	OldName   string
	NewDirFid proto.Fid
	NewName   string
}

// Type returns proto.TypeTrenameat.
func (m *Trenameat) Type() proto.MessageType { return proto.TypeTrenameat }

// EncodeTo writes the Trenameat body: olddirfid[4] oldname[s] newdirfid[4] newname[s].
func (m *Trenameat) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.OldDirFid)); err != nil {
		return fmt.Errorf("encode trenameat olddirfid: %w", err)
	}
	if err := proto.WriteString(w, m.OldName); err != nil {
		return fmt.Errorf("encode trenameat oldname: %w", err)
	}
	if err := proto.WriteUint32(w, uint32(m.NewDirFid)); err != nil {
		return fmt.Errorf("encode trenameat newdirfid: %w", err)
	}
	if err := proto.WriteString(w, m.NewName); err != nil {
		return fmt.Errorf("encode trenameat newname: %w", err)
	}
	return nil
}

// DecodeFrom reads the Trenameat body: olddirfid[4] oldname[s] newdirfid[4] newname[s].
func (m *Trenameat) DecodeFrom(r io.Reader) error {
	od, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode trenameat olddirfid: %w", err)
	}
	m.OldDirFid = proto.Fid(od)
	if m.OldName, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode trenameat oldname: %w", err)
	}
	nd, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode trenameat newdirfid: %w", err)
	}
	m.NewDirFid = proto.Fid(nd)
	if m.NewName, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode trenameat newname: %w", err)
	}
	return nil
}

// Rrenameat confirms a renameat operation. It has an empty body.
type Rrenameat struct{}

// Type returns proto.TypeRrenameat.
func (m *Rrenameat) Type() proto.MessageType { return proto.TypeRrenameat }

// EncodeTo writes nothing; Rrenameat has an empty body.
func (m *Rrenameat) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rrenameat has an empty body.
func (m *Rrenameat) DecodeFrom(_ io.Reader) error { return nil }

// Tunlinkat requests removing a file from the directory referenced by DirFid.
// Wire body: dirfid[4] name[s] flags[4].
type Tunlinkat struct {
	DirFid proto.Fid
	Name   string
	Flags  uint32
}

// Type returns proto.TypeTunlinkat.
func (m *Tunlinkat) Type() proto.MessageType { return proto.TypeTunlinkat }

// EncodeTo writes the Tunlinkat body: dirfid[4] name[s] flags[4].
func (m *Tunlinkat) EncodeTo(w io.Writer) error {
	if err := proto.WriteUint32(w, uint32(m.DirFid)); err != nil {
		return fmt.Errorf("encode tunlinkat dirfid: %w", err)
	}
	if err := proto.WriteString(w, m.Name); err != nil {
		return fmt.Errorf("encode tunlinkat name: %w", err)
	}
	if err := proto.WriteUint32(w, m.Flags); err != nil {
		return fmt.Errorf("encode tunlinkat flags: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tunlinkat body: dirfid[4] name[s] flags[4].
func (m *Tunlinkat) DecodeFrom(r io.Reader) error {
	v, err := proto.ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tunlinkat dirfid: %w", err)
	}
	m.DirFid = proto.Fid(v)
	if m.Name, err = proto.ReadString(r); err != nil {
		return fmt.Errorf("decode tunlinkat name: %w", err)
	}
	if m.Flags, err = proto.ReadUint32(r); err != nil {
		return fmt.Errorf("decode tunlinkat flags: %w", err)
	}
	return nil
}

// Runlinkat confirms an unlinkat operation. It has an empty body.
type Runlinkat struct{}

// Type returns proto.TypeRunlinkat.
func (m *Runlinkat) Type() proto.MessageType { return proto.TypeRunlinkat }

// EncodeTo writes nothing; Runlinkat has an empty body.
func (m *Runlinkat) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Runlinkat has an empty body.
func (m *Runlinkat) DecodeFrom(_ io.Reader) error { return nil }

// Compile-time interface compliance checks for all 9P2000.L message types.
var (
	_ proto.Message = (*Rlerror)(nil)
	_ proto.Message = (*Tstatfs)(nil)
	_ proto.Message = (*Rstatfs)(nil)
	_ proto.Message = (*Tlopen)(nil)
	_ proto.Message = (*Rlopen)(nil)
	_ proto.Message = (*Tlcreate)(nil)
	_ proto.Message = (*Rlcreate)(nil)
	_ proto.Message = (*Tsymlink)(nil)
	_ proto.Message = (*Rsymlink)(nil)
	_ proto.Message = (*Tmknod)(nil)
	_ proto.Message = (*Rmknod)(nil)
	_ proto.Message = (*Trename)(nil)
	_ proto.Message = (*Rrename)(nil)
	_ proto.Message = (*Treadlink)(nil)
	_ proto.Message = (*Rreadlink)(nil)
	_ proto.Message = (*Tgetattr)(nil)
	_ proto.Message = (*Rgetattr)(nil)
	_ proto.Message = (*Tsetattr)(nil)
	_ proto.Message = (*Rsetattr)(nil)
	_ proto.Message = (*Txattrwalk)(nil)
	_ proto.Message = (*Rxattrwalk)(nil)
	_ proto.Message = (*Txattrcreate)(nil)
	_ proto.Message = (*Rxattrcreate)(nil)
	_ proto.Message = (*Treaddir)(nil)
	_ proto.Message = (*Rreaddir)(nil)
	_ proto.Message = (*Tfsync)(nil)
	_ proto.Message = (*Rfsync)(nil)
	_ proto.Message = (*Tlock)(nil)
	_ proto.Message = (*Rlock)(nil)
	_ proto.Message = (*Tgetlock)(nil)
	_ proto.Message = (*Rgetlock)(nil)
	_ proto.Message = (*Tlink)(nil)
	_ proto.Message = (*Rlink)(nil)
	_ proto.Message = (*Tmkdir)(nil)
	_ proto.Message = (*Rmkdir)(nil)
	_ proto.Message = (*Trenameat)(nil)
	_ proto.Message = (*Rrenameat)(nil)
	_ proto.Message = (*Tunlinkat)(nil)
	_ proto.Message = (*Runlinkat)(nil)
)
