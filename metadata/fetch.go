package metadata

//go:generate go run go.uber.org/mock/mockgen -destination=../mocks/mock_fetcher.go -package=mocks github.com/kdwils/mgnx/metadata Fetcher

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/anacrolix/torrent/bencode"
)

// Fetcher fetches torrent metadata from a remote peer.
type Fetcher interface {
	Fetch(ctx context.Context, infohash [20]byte, addr net.TCPAddr) (*TorrentInfo, error)
}

// Dialer opens a TCP connection to a peer.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// TimeoutDialer wraps a Dialer with an explicit dial timeout.
type TimeoutDialer struct {
	Dialer      Dialer
	DialTimeout time.Duration
}

func (d TimeoutDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, d.DialTimeout)
	defer cancel()
	return d.Dialer.DialContext(ctx, network, address)
}

// Client is the production Fetcher. It holds a Dialer so the transport layer
// can be substituted in tests.
type Client struct {
	dialer Dialer
}

// NewClient creates a Client using the provided Dialer.
func NewClient(dialer Dialer) *Client {
	return &Client{dialer: dialer}
}

const MAX_SIZE = 10 * 1024 * 1024

// maxMsgSize is the upper bound on a single BEP-10 wire message. A malicious
// peer could send a 4-byte length of 0xFFFFFFFF; without this guard the
// make([]byte, length) below would attempt a ~4 GB allocation before the
// connection deadline fires.
const maxMsgSize = 16 * 1024 * 1024

type FileInfo struct {
	Path string
	Size int64
}

type TorrentInfo struct {
	Name      string
	Files     []FileInfo
	TotalSize int64
}

func (c *Client) Fetch(ctx context.Context, infohash [20]byte, addr net.TCPAddr) (*TorrentInfo, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(6 * time.Second)
	}

	conn, err := c.dialer.DialContext(ctx, "tcp4", addr.String())
	if err != nil {
		return nil, err
	}

	// Set SO_LINGER to quickly close stuck connections
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetLinger(0) //nolint:errcheck
	}

	defer conn.Close()

	conn.SetDeadline(deadline) //nolint:errcheck

	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	var peerID [20]byte
	rand.Read(peerID[:]) //nolint:errcheck

	if err := sendHandshake(rw.Writer, infohash, peerID); err != nil {
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		return nil, err
	}

	peerReserved, peerInfohash, err := readHandshake(rw.Reader)
	if err != nil {
		return nil, err
	}

	if peerReserved[5]&0x10 == 0 {
		return nil, errors.New("peer does not support extension protocol")
	}
	if peerInfohash != infohash {
		return nil, errors.New("infohash mismatch")
	}

	extHandshakePayload, err := bencode.Marshal(map[string]any{
		"m":    map[string]any{"ut_metadata": 1},
		"reqq": 250,
	})
	if err != nil {
		return nil, err
	}

	if err := sendMsg(rw.Writer, 20, 0, extHandshakePayload); err != nil {
		return nil, err
	}

	type extHandshakeMsg struct {
		M            map[string]int `bencode:"m"`
		MetadataSize int            `bencode:"metadata_size"`
	}

	var peerUTMetadataID int
	var totalSize int

	for {
		msgType, payload, err := readMsg(rw.Reader)
		if err != nil {
			return nil, err
		}
		if msgType != 20 {
			continue
		}
		if len(payload) == 0 {
			continue
		}
		if payload[0] != 0 {
			continue
		}
		var msg extHandshakeMsg
		if err := bencode.Unmarshal(payload[1:], &msg); err != nil {
			continue
		}
		peerUTMetadataID = msg.M["ut_metadata"]
		totalSize = msg.MetadataSize
		break
	}

	if peerUTMetadataID == 0 {
		return nil, errors.New("peer does not support ut_metadata")
	}
	if totalSize <= 0 || totalSize > MAX_SIZE {
		return nil, fmt.Errorf("invalid metadata size: %d", totalSize)
	}

	pieceSize := 16384
	numPieces := (totalSize + pieceSize - 1) / pieceSize
	assembled := make([]byte, 0, totalSize)

	for i := 0; i < numPieces; i++ {
		req, err := bencode.Marshal(map[string]any{
			"msg_type": 0,
			"piece":    i,
		})
		if err != nil {
			return nil, err
		}

		if err := sendMsg(rw.Writer, 20, byte(peerUTMetadataID), req); err != nil {
			return nil, err
		}

		for {
			msgType, payload, err := readMsg(rw.Reader)
			if err != nil {
				return nil, err
			}
			if msgType != 20 {
				continue
			}
			if len(payload) == 0 {
				continue
			}
			if payload[0] == 0 {
				continue
			}

			r := bytes.NewReader(payload[1:])
			dec := bencode.NewDecoder(r)

			type utMetadataMsgHeader struct {
				MsgType   int `bencode:"msg_type"`
				Piece     int `bencode:"piece"`
				TotalSize int `bencode:"total_size"`
			}

			var hdr utMetadataMsgHeader
			if err := dec.Decode(&hdr); err != nil {
				continue
			}

			if hdr.MsgType != 1 || hdr.Piece != i {
				continue
			}

			pieceData, err := io.ReadAll(r)
			if err != nil {
				return nil, err
			}
			assembled = append(assembled, pieceData...)
			break
		}
	}

	hash := sha1.Sum(assembled)
	if hash != infohash {
		return nil, errors.New("metadata sha1 mismatch")
	}

	return decodeInfo(assembled)
}

func sendHandshake(w io.Writer, infohash [20]byte, peerID [20]byte) error {
	var buf [68]byte
	buf[0] = 0x13
	copy(buf[1:20], "BitTorrent protocol")
	buf[25] |= 0x10
	copy(buf[28:48], infohash[:])
	copy(buf[48:68], peerID[:])
	_, err := w.Write(buf[:])
	return err
}

func readHandshake(r io.Reader) (reserved [8]byte, infohash [20]byte, err error) {
	var buf [68]byte
	if _, err = io.ReadFull(r, buf[:]); err != nil {
		return
	}
	if buf[0] != 0x13 {
		err = errors.New("invalid pstrlen")
		return
	}
	if string(buf[1:20]) != "BitTorrent protocol" {
		err = errors.New("invalid pstr")
		return
	}
	copy(reserved[:], buf[20:28])
	copy(infohash[:], buf[28:48])
	return
}

func sendMsg(w io.Writer, msgType byte, extID byte, payload []byte) error {
	length := uint32(1 + 1 + len(payload))
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], length)
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write([]byte{msgType, extID}); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	if bw, ok := w.(*bufio.Writer); ok {
		return bw.Flush()
	}
	return nil
}

func readMsg(r io.Reader) (msgType byte, payload []byte, err error) {
	var lenBuf [4]byte
	if _, err = io.ReadFull(r, lenBuf[:]); err != nil {
		return
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 {
		return 0, nil, nil
	}
	if length > maxMsgSize {
		return 0, nil, fmt.Errorf("message too large: %d bytes", length)
	}
	body := make([]byte, length)
	if _, err = io.ReadFull(r, body); err != nil {
		return
	}
	msgType = body[0]
	payload = body[1:]
	return
}

type rawFile struct {
	Path   []string `bencode:"path"`
	Length int64    `bencode:"length"`
}

type rawInfo struct {
	Name        string    `bencode:"name"`
	Length      int64     `bencode:"length"`
	Files       []rawFile `bencode:"files"`
	PieceLength int64     `bencode:"piece length"`
}

func decodeInfo(data []byte) (*TorrentInfo, error) {
	var info rawInfo
	if err := bencode.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	result := &TorrentInfo{Name: info.Name}

	if len(info.Files) == 0 {
		result.TotalSize = info.Length
		result.Files = []FileInfo{{Path: info.Name, Size: info.Length}}
		return result, nil
	}

	for _, f := range info.Files {
		result.TotalSize += f.Length
		result.Files = append(result.Files, FileInfo{
			Path: strings.Join(f.Path, "/"),
			Size: f.Length,
		})
	}
	return result, nil
}
