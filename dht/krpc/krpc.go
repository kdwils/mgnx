package krpc

// Msg is the top-level KRPC message envelope.
type Msg struct {
	T  string   `bencode:"t"`
	Y  string   `bencode:"y"`
	Q  string   `bencode:"q,omitempty"`
	A  *MsgArgs `bencode:"a,omitempty"`
	R  *Return  `bencode:"r,omitempty"`
	E  []any    `bencode:"e,omitempty"`
	V  string   `bencode:"v,omitempty"`
	IP string   `bencode:"ip,omitempty"`
}

// MsgArgs holds query arguments (union of fields across all query types).
type MsgArgs struct {
	ID          string `bencode:"id"`
	Target      string `bencode:"target,omitempty"`
	InfoHash    string `bencode:"info_hash,omitempty"`
	Token       string `bencode:"token,omitempty"`
	Port        int    `bencode:"port,omitempty"`
	ImpliedPort *int   `bencode:"implied_port,omitempty"`
}

// Return holds the response body (union of fields across all response types).
type Return struct {
	ID       string   `bencode:"id"`
	Nodes    string   `bencode:"nodes,omitempty"`
	Values   []string `bencode:"values,omitempty"`
	Token    string   `bencode:"token,omitempty"`
	Samples  string   `bencode:"samples,omitempty"`
	Interval int      `bencode:"interval,omitempty"`
	Num      int      `bencode:"num,omitempty"`
}

// KRPC error codes per BEP-05 §KRPC Protocol and BEP-51 §3.
const (
	ErrGeneric  = 201
	ErrServer   = 202
	ErrProtocol = 203
	ErrMethod   = 204
)

// IsMethodUnknown reports whether msg is a KRPC 204 "method unknown" error.
// Per BEP-51 §3, nodes that don't implement sample_infohashes return this code.
func IsMethodUnknown(msg *Msg) bool {
	if msg == nil || len(msg.E) < 1 {
		return false
	}
	code, ok := msg.E[0].(int64)
	return ok && code == ErrMethod
}
