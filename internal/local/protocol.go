package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
)

const (
	SocketPath  = "/Library/Application Support/ai.vult.ledger/socket/service.sock"
	BinaryPath  = "/Library/PrivilegedHelperTools/ai.vult.ledger/vult"
	DataPath    = "/Library/Application Support/ai.vult.ledger/data"
	ServiceUser = "_vultledger"
	MaxMessage  = 128 << 20
)

type Request struct {
	Method        string          `json:"method"`
	Authorization string          `json:"authorization,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}
type Header struct {
	Method        string `json:"method"`
	Authorization string `json:"authorization,omitempty"`
	Size          int    `json:"size"`
	StreamSize    int64  `json:"stream_size,omitempty"`
}
type Response struct {
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	StreamSize int64           `json:"stream_size,omitempty"`
}
type Client struct {
	Socket      string
	ExpectedUID uint32
}

func NewClient() (Client, error) {
	u, e := user.Lookup(ServiceUser)
	if e != nil {
		return Client{}, errors.New("vult service is not installed")
	}
	uid, e := strconv.ParseUint(u.Uid, 10, 32)
	if e != nil || uid == 0 || uint32(uid) == uint32(os.Getuid()) {
		return Client{}, errors.New("dedicated service identity required")
	}
	return Client{Socket: SocketPath, ExpectedUID: uint32(uid)}, nil
}
func (c Client) Call(ctx context.Context, method, authorization string, payload any) (json.RawMessage, error) {
	b, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	conn, e := c.dial(ctx)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	header, e := json.Marshal(Header{Method: method, Authorization: authorization, Size: len(b)})
	if e != nil || len(b) > MaxMessage {
		return nil, errors.New("request exceeds supported size")
	}
	if _, e = conn.Write(append(header, '\n')); e != nil {
		return nil, errors.New("request outcome unresolved")
	}
	if _, e = conn.Write(b); e != nil {
		return nil, errors.New("request outcome unresolved")
	}
	reply, e := ReadMessage(conn)
	if e != nil {
		return nil, errors.New("request outcome unresolved")
	}
	var response Response
	if strictjson.Decode(reply, &response) != nil {
		return nil, errors.New("invalid service response")
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	if response.Result == nil {
		return nil, errors.New("missing service result")
	}
	return response.Result, nil
}
func (c Client) dial(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, e := dialer.DialContext(ctx, "unix", c.Socket)
	if e != nil {
		return nil, errors.New("local service unavailable")
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, errors.New("local socket required")
	}
	uid, e := PeerUID(unixConn)
	if e != nil || uid != c.ExpectedUID {
		conn.Close()
		return nil, errors.New("untrusted local service")
	}
	deadline := time.Now().Add(2 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if e = conn.SetDeadline(deadline); e != nil {
		conn.Close()
		return nil, errors.New("local service unavailable")
	}
	return conn, nil
}
func ReadMessage(r io.Reader) ([]byte, error) {
	reader := bufio.NewReaderSize(io.LimitReader(r, MaxMessage+2), 64<<10)
	b, e := reader.ReadBytes('\n')
	if e != nil || len(b) > MaxMessage || len(b) < 2 {
		return nil, errors.New("invalid local message")
	}
	return b[:len(b)-1], nil
}
