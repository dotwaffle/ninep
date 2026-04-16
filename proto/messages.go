package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Tversion is the first message sent on a 9P connection to negotiate the
// protocol version and maximum message size.
type Tversion struct {
	Msize   uint32
	Version string
}

// Type returns TypeTversion.
func (m *Tversion) Type() MessageType { return TypeTversion }

// EncodeTo writes the Tversion body: msize[4] + version[s].
func (m *Tversion) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, m.Msize); err != nil {
		return fmt.Errorf("encode tversion msize: %w", err)
	}
	if err := WriteString(w, m.Version); err != nil {
		return fmt.Errorf("encode tversion version: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tversion body: msize[4] + version[s].
func (m *Tversion) DecodeFrom(r io.Reader) error {
	var err error
	if m.Msize, err = ReadUint32(r); err != nil {
		return fmt.Errorf("decode tversion msize: %w", err)
	}
	if m.Version, err = ReadString(r); err != nil {
		return fmt.Errorf("decode tversion version: %w", err)
	}
	return nil
}

// Rversion is the server's response to Tversion, confirming the negotiated
// protocol version and message size.
type Rversion struct {
	Msize   uint32
	Version string
}

// Type returns TypeRversion.
func (m *Rversion) Type() MessageType { return TypeRversion }

// EncodeTo writes the Rversion body: msize[4] + version[s].
func (m *Rversion) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, m.Msize); err != nil {
		return fmt.Errorf("encode rversion msize: %w", err)
	}
	if err := WriteString(w, m.Version); err != nil {
		return fmt.Errorf("encode rversion version: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rversion body: msize[4] + version[s].
func (m *Rversion) DecodeFrom(r io.Reader) error {
	var err error
	if m.Msize, err = ReadUint32(r); err != nil {
		return fmt.Errorf("decode rversion msize: %w", err)
	}
	if m.Version, err = ReadString(r); err != nil {
		return fmt.Errorf("decode rversion version: %w", err)
	}
	return nil
}

// Tauth initiates an authentication handshake. The NUname field is present in
// both 9P2000.L and 9P2000.u dialects.
type Tauth struct {
	Afid   Fid
	Uname  string
	Aname  string
	NUname uint32
}

// Type returns TypeTauth.
func (m *Tauth) Type() MessageType { return TypeTauth }

// EncodeTo writes the Tauth body: afid[4] + uname[s] + aname[s] + n_uname[4].
func (m *Tauth) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(m.Afid)); err != nil {
		return fmt.Errorf("encode tauth afid: %w", err)
	}
	if err := WriteString(w, m.Uname); err != nil {
		return fmt.Errorf("encode tauth uname: %w", err)
	}
	if err := WriteString(w, m.Aname); err != nil {
		return fmt.Errorf("encode tauth aname: %w", err)
	}
	if err := WriteUint32(w, m.NUname); err != nil {
		return fmt.Errorf("encode tauth n_uname: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tauth body: afid[4] + uname[s] + aname[s] + n_uname[4].
func (m *Tauth) DecodeFrom(r io.Reader) error {
	afid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tauth afid: %w", err)
	}
	m.Afid = Fid(afid)
	if m.Uname, err = ReadString(r); err != nil {
		return fmt.Errorf("decode tauth uname: %w", err)
	}
	if m.Aname, err = ReadString(r); err != nil {
		return fmt.Errorf("decode tauth aname: %w", err)
	}
	if m.NUname, err = ReadUint32(r); err != nil {
		return fmt.Errorf("decode tauth n_uname: %w", err)
	}
	return nil
}

// Rauth is the server's response to Tauth, providing the authentication QID.
type Rauth struct {
	AQid QID
}

// Type returns TypeRauth.
func (m *Rauth) Type() MessageType { return TypeRauth }

// EncodeTo writes the Rauth body: aqid[13].
func (m *Rauth) EncodeTo(w io.Writer) error {
	if err := WriteQID(w, m.AQid); err != nil {
		return fmt.Errorf("encode rauth aqid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rauth body: aqid[13].
func (m *Rauth) DecodeFrom(r io.Reader) error {
	var err error
	if m.AQid, err = ReadQID(r); err != nil {
		return fmt.Errorf("decode rauth aqid: %w", err)
	}
	return nil
}

// Tattach attaches a fid to the root of a file tree. The NUname field is
// present in both 9P2000.L and 9P2000.u dialects.
type Tattach struct {
	Fid    Fid
	Afid   Fid
	Uname  string
	Aname  string
	NUname uint32
}

// Type returns TypeTattach.
func (m *Tattach) Type() MessageType { return TypeTattach }

// EncodeTo writes the Tattach body: fid[4] + afid[4] + uname[s] + aname[s] + n_uname[4].
func (m *Tattach) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tattach fid: %w", err)
	}
	if err := WriteUint32(w, uint32(m.Afid)); err != nil {
		return fmt.Errorf("encode tattach afid: %w", err)
	}
	if err := WriteString(w, m.Uname); err != nil {
		return fmt.Errorf("encode tattach uname: %w", err)
	}
	if err := WriteString(w, m.Aname); err != nil {
		return fmt.Errorf("encode tattach aname: %w", err)
	}
	if err := WriteUint32(w, m.NUname); err != nil {
		return fmt.Errorf("encode tattach n_uname: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tattach body: fid[4] + afid[4] + uname[s] + aname[s] + n_uname[4].
func (m *Tattach) DecodeFrom(r io.Reader) error {
	fid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tattach fid: %w", err)
	}
	m.Fid = Fid(fid)
	afid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tattach afid: %w", err)
	}
	m.Afid = Fid(afid)
	if m.Uname, err = ReadString(r); err != nil {
		return fmt.Errorf("decode tattach uname: %w", err)
	}
	if m.Aname, err = ReadString(r); err != nil {
		return fmt.Errorf("decode tattach aname: %w", err)
	}
	if m.NUname, err = ReadUint32(r); err != nil {
		return fmt.Errorf("decode tattach n_uname: %w", err)
	}
	return nil
}

// Rattach is the server's response to Tattach, providing the root QID.
type Rattach struct {
	QID QID
}

// Type returns TypeRattach.
func (m *Rattach) Type() MessageType { return TypeRattach }

// EncodeTo writes the Rattach body: qid[13].
func (m *Rattach) EncodeTo(w io.Writer) error {
	if err := WriteQID(w, m.QID); err != nil {
		return fmt.Errorf("encode rattach qid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rattach body: qid[13].
func (m *Rattach) DecodeFrom(r io.Reader) error {
	var err error
	if m.QID, err = ReadQID(r); err != nil {
		return fmt.Errorf("decode rattach qid: %w", err)
	}
	return nil
}

// Tflush requests cancellation of a pending request identified by OldTag.
type Tflush struct {
	OldTag Tag
}

// Type returns TypeTflush.
func (m *Tflush) Type() MessageType { return TypeTflush }

// EncodeTo writes the Tflush body: oldtag[2].
func (m *Tflush) EncodeTo(w io.Writer) error {
	if err := WriteUint16(w, uint16(m.OldTag)); err != nil {
		return fmt.Errorf("encode tflush oldtag: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tflush body: oldtag[2].
func (m *Tflush) DecodeFrom(r io.Reader) error {
	tag, err := ReadUint16(r)
	if err != nil {
		return fmt.Errorf("decode tflush oldtag: %w", err)
	}
	m.OldTag = Tag(tag)
	return nil
}

// Rflush confirms cancellation of a flushed request. It has an empty body.
type Rflush struct{}

// Type returns TypeRflush.
func (m *Rflush) Type() MessageType { return TypeRflush }

// EncodeTo writes nothing; Rflush has an empty body.
func (m *Rflush) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rflush has an empty body.
func (m *Rflush) DecodeFrom(_ io.Reader) error { return nil }

// Twalk walks a path from Fid, producing NewFid at the walked-to node.
// Names must contain at most MaxWalkElements entries.
type Twalk struct {
	Fid    Fid
	NewFid Fid
	Names  []string
}

// Type returns TypeTwalk.
func (m *Twalk) Type() MessageType { return TypeTwalk }

// EncodeTo writes the Twalk body: fid[4] + newfid[4] + nwname[2] + nwname*(wname[s]).
func (m *Twalk) EncodeTo(w io.Writer) error {
	if len(m.Names) > MaxWalkElements {
		return fmt.Errorf("walk name count %d exceeds max %d", len(m.Names), MaxWalkElements)
	}
	if err := WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode twalk fid: %w", err)
	}
	if err := WriteUint32(w, uint32(m.NewFid)); err != nil {
		return fmt.Errorf("encode twalk newfid: %w", err)
	}
	if err := WriteUint16(w, uint16(len(m.Names))); err != nil {
		return fmt.Errorf("encode twalk nwname: %w", err)
	}
	for i, name := range m.Names {
		if err := WriteString(w, name); err != nil {
			return fmt.Errorf("encode twalk name[%d]: %w", i, err)
		}
	}
	return nil
}

// DecodeFrom reads the Twalk body: fid[4] + newfid[4] + nwname[2] + nwname*(wname[s]).
func (m *Twalk) DecodeFrom(r io.Reader) error {
	fid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode twalk fid: %w", err)
	}
	m.Fid = Fid(fid)
	newfid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode twalk newfid: %w", err)
	}
	m.NewFid = Fid(newfid)
	nwname, err := ReadUint16(r)
	if err != nil {
		return fmt.Errorf("decode twalk nwname: %w", err)
	}
	if int(nwname) > MaxWalkElements {
		return fmt.Errorf("walk name count %d exceeds max %d", nwname, MaxWalkElements)
	}
	m.Names = make([]string, nwname)
	for i := range m.Names {
		if m.Names[i], err = ReadString(r); err != nil {
			return fmt.Errorf("decode twalk name[%d]: %w", i, err)
		}
	}
	return nil
}

// Rwalk is the server's response to Twalk, containing one QID per
// successfully walked path element.
type Rwalk struct {
	QIDs []QID
}

// Type returns TypeRwalk.
func (m *Rwalk) Type() MessageType { return TypeRwalk }

// EncodeTo writes the Rwalk body: nwqid[2] + nwqid*(qid[13]).
func (m *Rwalk) EncodeTo(w io.Writer) error {
	if len(m.QIDs) > MaxWalkElements {
		return fmt.Errorf("walk qid count %d exceeds max %d", len(m.QIDs), MaxWalkElements)
	}
	if err := WriteUint16(w, uint16(len(m.QIDs))); err != nil {
		return fmt.Errorf("encode rwalk nwqid: %w", err)
	}
	for i, qid := range m.QIDs {
		if err := WriteQID(w, qid); err != nil {
			return fmt.Errorf("encode rwalk qid[%d]: %w", i, err)
		}
	}
	return nil
}

// DecodeFrom reads the Rwalk body: nwqid[2] + nwqid*(qid[13]).
func (m *Rwalk) DecodeFrom(r io.Reader) error {
	nwqid, err := ReadUint16(r)
	if err != nil {
		return fmt.Errorf("decode rwalk nwqid: %w", err)
	}
	if int(nwqid) > MaxWalkElements {
		return fmt.Errorf("walk qid count %d exceeds max %d", nwqid, MaxWalkElements)
	}
	m.QIDs = make([]QID, nwqid)
	for i := range m.QIDs {
		if m.QIDs[i], err = ReadQID(r); err != nil {
			return fmt.Errorf("decode rwalk qid[%d]: %w", i, err)
		}
	}
	return nil
}

// Tread requests reading Count bytes starting at Offset from Fid.
type Tread struct {
	Fid    Fid
	Offset uint64
	Count  uint32
}

// Type returns TypeTread.
func (m *Tread) Type() MessageType { return TypeTread }

// EncodeTo writes the Tread body: fid[4] + offset[8] + count[4].
func (m *Tread) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tread fid: %w", err)
	}
	if err := WriteUint64(w, m.Offset); err != nil {
		return fmt.Errorf("encode tread offset: %w", err)
	}
	if err := WriteUint32(w, m.Count); err != nil {
		return fmt.Errorf("encode tread count: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tread body: fid[4] + offset[8] + count[4].
func (m *Tread) DecodeFrom(r io.Reader) error {
	fid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tread fid: %w", err)
	}
	m.Fid = Fid(fid)
	if m.Offset, err = ReadUint64(r); err != nil {
		return fmt.Errorf("decode tread offset: %w", err)
	}
	if m.Count, err = ReadUint32(r); err != nil {
		return fmt.Errorf("decode tread count: %w", err)
	}
	return nil
}

// Payloader is implemented by response messages that carry a large opaque
// payload (typically the user data portion of Rread/Rreaddir). The
// server's inline response writer detects Payloaders and issues the
// payload as a separate net.Buffers entry so the header, fixed body, and
// payload are emitted in a single writev without copying the payload into
// the pooled body buffer.
//
// Contract:
//   - EncodeFixed writes only the non-payload part of the body. For Rread
//     that is the 4-byte count prefix; for Rreaddir likewise.
//   - Payload returns the []byte that should immediately follow the fixed
//     body on the wire. The slice may alias a pooled buffer; the server
//     uses the Releaser interface to return it after writev completes.
//
// Implementations MUST still implement a correct full-message EncodeTo
// (Message interface) for non-server callers such as client-side encoders
// and tests.
type Payloader interface {
	EncodeFixed(w io.Writer) error
	Payload() []byte
}

// Rread is the server's response to Tread, containing the requested data.
type Rread struct {
	Data []byte
}

// Type returns TypeRread.
func (m *Rread) Type() MessageType { return TypeRread }

// EncodeFixed implements Payloader. Writes only the 4-byte count prefix;
// the server's inline response writer emits m.Data as a separate
// net.Buffers entry via a single writev.
func (m *Rread) EncodeFixed(w io.Writer) error {
	return WriteUint32(w, uint32(len(m.Data)))
}

// Payload implements Payloader. Returns m.Data so the inline response
// writer can place it directly into net.Buffers without an intermediate
// copy.
func (m *Rread) Payload() []byte { return m.Data }

// EncodeTo writes the Rread body: count[4] + data[count].
func (m *Rread) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(len(m.Data))); err != nil {
		return fmt.Errorf("encode rread count: %w", err)
	}
	if len(m.Data) > 0 {
		if _, err := w.Write(m.Data); err != nil {
			return fmt.Errorf("encode rread data: %w", err)
		}
	}
	return nil
}

// DecodeFrom reads the Rread body: count[4] + data[count].
func (m *Rread) DecodeFrom(r io.Reader) error {
	count, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode rread count: %w", err)
	}
	if count > MaxDataSize {
		return fmt.Errorf("decode rread count %d exceeds maximum %d", count, MaxDataSize)
	}
	m.Data = make([]byte, count)
	if count > 0 {
		if _, err := io.ReadFull(r, m.Data); err != nil {
			return fmt.Errorf("decode rread data: %w", err)
		}
	}
	return nil
}

// Twrite requests writing data to Fid at the given Offset.
type Twrite struct {
	Fid    Fid
	Offset uint64
	Data   []byte
}

// Type returns TypeTwrite.
func (m *Twrite) Type() MessageType { return TypeTwrite }

// EncodeTo writes the Twrite body: fid[4] + offset[8] + count[4] + data[count].
func (m *Twrite) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode twrite fid: %w", err)
	}
	if err := WriteUint64(w, m.Offset); err != nil {
		return fmt.Errorf("encode twrite offset: %w", err)
	}
	if err := WriteUint32(w, uint32(len(m.Data))); err != nil {
		return fmt.Errorf("encode twrite count: %w", err)
	}
	if len(m.Data) > 0 {
		if _, err := w.Write(m.Data); err != nil {
			return fmt.Errorf("encode twrite data: %w", err)
		}
	}
	return nil
}

// DecodeFrom reads the Twrite body: fid[4] + offset[8] + count[4] + data[count].
// m.Data is allocated and populated from r; callers can freely reuse r's
// underlying storage after DecodeFrom returns.
func (m *Twrite) DecodeFrom(r io.Reader) error {
	fid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode twrite fid: %w", err)
	}
	m.Fid = Fid(fid)
	if m.Offset, err = ReadUint64(r); err != nil {
		return fmt.Errorf("decode twrite offset: %w", err)
	}
	count, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode twrite count: %w", err)
	}
	if count > MaxDataSize {
		return fmt.Errorf("decode twrite count %d exceeds maximum %d", count, MaxDataSize)
	}
	m.Data = make([]byte, count)
	if count > 0 {
		if _, err := io.ReadFull(r, m.Data); err != nil {
			return fmt.Errorf("decode twrite data: %w", err)
		}
	}
	return nil
}

// DecodeFromBuf is a zero-copy alternative to DecodeFrom. m.Data aliases a
// sub-slice of b — the caller MUST keep b alive (and unmodified) for as
// long as m.Data is read. Intended for the server's handleRequest recv
// loop, which holds a pooled buffer and releases it only after the
// handler returns.
//
// Body layout: fid[4] + offset[8] + count[4] + data[count].
func (m *Twrite) DecodeFromBuf(b []byte) error {
	if len(b) < 16 {
		return fmt.Errorf("decode twrite: body too short (%d < 16)", len(b))
	}
	m.Fid = Fid(binary.LittleEndian.Uint32(b[0:4]))
	m.Offset = binary.LittleEndian.Uint64(b[4:12])
	count := binary.LittleEndian.Uint32(b[12:16])
	if count > MaxDataSize {
		return fmt.Errorf("decode twrite count %d exceeds maximum %d", count, MaxDataSize)
	}
	if uint32(len(b)-16) < count {
		return fmt.Errorf("decode twrite: data short, want %d have %d", count, len(b)-16)
	}
	m.Data = b[16 : 16+count] // zero-copy alias; caller owns lifetime of b
	return nil
}

// Rwrite is the server's response to Twrite, reporting how many bytes were
// written.
type Rwrite struct {
	Count uint32
}

// Type returns TypeRwrite.
func (m *Rwrite) Type() MessageType { return TypeRwrite }

// EncodeTo writes the Rwrite body: count[4].
func (m *Rwrite) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, m.Count); err != nil {
		return fmt.Errorf("encode rwrite count: %w", err)
	}
	return nil
}

// DecodeFrom reads the Rwrite body: count[4].
func (m *Rwrite) DecodeFrom(r io.Reader) error {
	var err error
	if m.Count, err = ReadUint32(r); err != nil {
		return fmt.Errorf("decode rwrite count: %w", err)
	}
	return nil
}

// Tclunk requests that the server forget about a fid.
type Tclunk struct {
	Fid Fid
}

// Type returns TypeTclunk.
func (m *Tclunk) Type() MessageType { return TypeTclunk }

// EncodeTo writes the Tclunk body: fid[4].
func (m *Tclunk) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tclunk fid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tclunk body: fid[4].
func (m *Tclunk) DecodeFrom(r io.Reader) error {
	fid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tclunk fid: %w", err)
	}
	m.Fid = Fid(fid)
	return nil
}

// Rclunk confirms a clunk. It has an empty body.
type Rclunk struct{}

// Type returns TypeRclunk.
func (m *Rclunk) Type() MessageType { return TypeRclunk }

// EncodeTo writes nothing; Rclunk has an empty body.
func (m *Rclunk) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rclunk has an empty body.
func (m *Rclunk) DecodeFrom(_ io.Reader) error { return nil }

// Tremove requests removal of the file associated with Fid.
type Tremove struct {
	Fid Fid
}

// Type returns TypeTremove.
func (m *Tremove) Type() MessageType { return TypeTremove }

// EncodeTo writes the Tremove body: fid[4].
func (m *Tremove) EncodeTo(w io.Writer) error {
	if err := WriteUint32(w, uint32(m.Fid)); err != nil {
		return fmt.Errorf("encode tremove fid: %w", err)
	}
	return nil
}

// DecodeFrom reads the Tremove body: fid[4].
func (m *Tremove) DecodeFrom(r io.Reader) error {
	fid, err := ReadUint32(r)
	if err != nil {
		return fmt.Errorf("decode tremove fid: %w", err)
	}
	m.Fid = Fid(fid)
	return nil
}

// Rremove confirms removal. It has an empty body.
type Rremove struct{}

// Type returns TypeRremove.
func (m *Rremove) Type() MessageType { return TypeRremove }

// EncodeTo writes nothing; Rremove has an empty body.
func (m *Rremove) EncodeTo(_ io.Writer) error { return nil }

// DecodeFrom reads nothing; Rremove has an empty body.
func (m *Rremove) DecodeFrom(_ io.Reader) error { return nil }

// Compile-time interface compliance checks.
var (
	_ Message = (*Tversion)(nil)
	_ Message = (*Rversion)(nil)
	_ Message = (*Tauth)(nil)
	_ Message = (*Rauth)(nil)
	_ Message = (*Tattach)(nil)
	_ Message = (*Rattach)(nil)
	_ Message = (*Tflush)(nil)
	_ Message = (*Rflush)(nil)
	_ Message = (*Twalk)(nil)
	_ Message = (*Rwalk)(nil)
	_ Message = (*Tread)(nil)
	_ Message = (*Rread)(nil)
	_ Message = (*Twrite)(nil)
	_ Message = (*Rwrite)(nil)
	_ Message = (*Tclunk)(nil)
	_ Message = (*Rclunk)(nil)
	_ Message = (*Tremove)(nil)
	_ Message = (*Rremove)(nil)
)
