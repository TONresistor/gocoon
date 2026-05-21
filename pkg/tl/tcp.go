package tl

// TCP packet wrapper layer from cocoon_api.tl:
//
//	tcp.ping id:long = tcp.Packet
//	tcp.pong id:long = tcp.Packet
//	tcp.packet data:bytes = tcp.Packet
//	tcp.queryAnswer id:long data:bytes = tcp.Packet
//	tcp.queryError id:long code:int message:string = tcp.Packet
//	tcp.query id:long data:bytes = tcp.Packet
//	tcp.connected id:long = tcp.Packet
//	tcp.connect id:long = tcp.Packet
//
// Constructor IDs computed via CRC32 of canonical signature (verified
// algorithm from research agent, same approach as the cocoon_api types).

import (
	"hash/crc32"
)

// tcp.* constructor IDs computed at init time from canonical signatures.
// Matches the algorithm verified against cocoon_api.tlo.
var (
	IDTCPPing        = crc32.ChecksumIEEE([]byte("tcp.ping id:long = tcp.Packet"))
	IDTCPPong        = crc32.ChecksumIEEE([]byte("tcp.pong id:long = tcp.Packet"))
	IDTCPPacket      = crc32.ChecksumIEEE([]byte("tcp.packet data:bytes = tcp.Packet"))
	IDTCPQueryAnswer = crc32.ChecksumIEEE([]byte("tcp.queryAnswer id:long data:bytes = tcp.Packet"))
	IDTCPQueryError  = crc32.ChecksumIEEE([]byte("tcp.queryError id:long code:int message:string = tcp.Packet"))
	IDTCPQuery       = crc32.ChecksumIEEE([]byte("tcp.query id:long data:bytes = tcp.Packet"))
	IDTCPConnected   = crc32.ChecksumIEEE([]byte("tcp.connected id:long = tcp.Packet"))
	IDTCPConnect     = crc32.ChecksumIEEE([]byte("tcp.connect id:long = tcp.Packet"))
)

// TCPPacket is the discriminated union of payloads carried on the wire.
// Read/Write functions are below; callers usually use the EncodeTCPQuery/etc.
// helpers and DecodeTCPPacket for receive.

// EncodeTCPConnect emits a `tcp.connect id:long` payload (already framed by
// FramedConn wrapper).
func EncodeTCPConnect(id int64) []byte {
	w := NewWriterCap(12)
	w.WriteUint32(IDTCPConnect)
	w.WriteInt64(id)
	return w.Bytes()
}

// EncodeTCPConnected emits a `tcp.connected id:long`.
func EncodeTCPConnected(id int64) []byte {
	w := NewWriterCap(12)
	w.WriteUint32(IDTCPConnected)
	w.WriteInt64(id)
	return w.Bytes()
}

// EncodeTCPQuery wraps a TL function payload in a tcp.query envelope.
//
// Wire layout: [u32: IDTCPQuery][i64: id][bytes: data]
func EncodeTCPQuery(queryID int64, data []byte) []byte {
	w := NewWriterCap(12 + len(data) + 4)
	w.WriteUint32(IDTCPQuery)
	w.WriteInt64(queryID)
	w.WriteBytes(data)
	return w.Bytes()
}

// EncodeTCPPacket wraps fire-and-forget data.
func EncodeTCPPacket(data []byte) []byte {
	w := NewWriterCap(8 + len(data))
	w.WriteUint32(IDTCPPacket)
	w.WriteBytes(data)
	return w.Bytes()
}

// EncodeTCPPing emits a tcp.ping with a random id chosen by caller.
func EncodeTCPPing(id int64) []byte {
	w := NewWriterCap(12)
	w.WriteUint32(IDTCPPing)
	w.WriteInt64(id)
	return w.Bytes()
}

// EncodeTCPPong emits a tcp.pong with id echoing the ping.
func EncodeTCPPong(id int64) []byte {
	w := NewWriterCap(12)
	w.WriteUint32(IDTCPPong)
	w.WriteInt64(id)
	return w.Bytes()
}

// TCPPacketKind identifies the variant of a decoded packet.
type TCPPacketKind int

const (
	TCPKindUnknown TCPPacketKind = iota
	TCPKindPing
	TCPKindPong
	TCPKindConnect
	TCPKindConnected
	TCPKindPacket
	TCPKindQuery
	TCPKindQueryAnswer
	TCPKindQueryError
)

// DecodedTCPPacket is the parsed result of one frame payload.
type DecodedTCPPacket struct {
	Kind    TCPPacketKind
	ID      int64  // ping/pong/connect/connected/query/queryAnswer/queryError
	Data    []byte // packet/query/queryAnswer
	ErrCode int32  // queryError
	ErrMsg  string // queryError
}

// DecodeTCPPacket parses a frame payload into a DecodedTCPPacket.
func DecodeTCPPacket(payload []byte) (*DecodedTCPPacket, error) {
	r := NewReader(payload)
	id, err := r.ReadUint32()
	if err != nil {
		return nil, err
	}
	out := &DecodedTCPPacket{}
	switch id {
	case IDTCPPing:
		out.Kind = TCPKindPing
		out.ID, err = r.ReadInt64()
	case IDTCPPong:
		out.Kind = TCPKindPong
		out.ID, err = r.ReadInt64()
	case IDTCPConnect:
		out.Kind = TCPKindConnect
		out.ID, err = r.ReadInt64()
	case IDTCPConnected:
		out.Kind = TCPKindConnected
		out.ID, err = r.ReadInt64()
	case IDTCPPacket:
		out.Kind = TCPKindPacket
		out.Data, err = r.ReadBytes()
	case IDTCPQuery:
		out.Kind = TCPKindQuery
		if out.ID, err = r.ReadInt64(); err == nil {
			out.Data, err = r.ReadBytes()
		}
	case IDTCPQueryAnswer:
		out.Kind = TCPKindQueryAnswer
		if out.ID, err = r.ReadInt64(); err == nil {
			out.Data, err = r.ReadBytes()
		}
	case IDTCPQueryError:
		out.Kind = TCPKindQueryError
		if out.ID, err = r.ReadInt64(); err == nil {
			var c int32
			c, err = r.ReadInt32()
			if err == nil {
				out.ErrCode = c
				out.ErrMsg, err = r.ReadString()
			}
		}
	default:
		out.Kind = TCPKindUnknown
		return out, ErrUnknownConstructor
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
