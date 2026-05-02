package tl

import (
	"bytes"
	"testing"
)

func TestTCPConnectRoundTrip(t *testing.T) {
	payload := EncodeTCPConnect(0x1234567890abcdef)
	pkt, err := DecodeTCPPacket(payload)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Kind != TCPKindConnect {
		t.Errorf("kind: %v", pkt.Kind)
	}
	if pkt.ID != 0x1234567890abcdef {
		t.Errorf("id: %x", pkt.ID)
	}
}

func TestTCPQueryRoundTrip(t *testing.T) {
	body := []byte{0xde, 0xad, 0xbe, 0xef}
	payload := EncodeTCPQuery(42, body)
	pkt, err := DecodeTCPPacket(payload)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Kind != TCPKindQuery {
		t.Errorf("kind: %v", pkt.Kind)
	}
	if pkt.ID != 42 {
		t.Errorf("id: %d", pkt.ID)
	}
	if !bytes.Equal(pkt.Data, body) {
		t.Errorf("data mismatch")
	}
}

func TestTCPQueryDataOffsetMatchesTLBytes(t *testing.T) {
	// tcp.query is [ctor:4][query_id:8][data:bytes]. For payloads shorter
	// than 254 bytes, TL bytes uses a one-byte length header, so data starts
	// at offset 13, not at a four-byte-aligned offset such as 16. Alignment
	// padding is after the bytes payload.
	body := []byte{0xf4, 0xa0, 0x5f, 0xff, 0x64, 0xca, 0xfd, 0x40}
	payload := EncodeTCPQuery(0x0102030405060708, body)

	if got, want := payload[12], byte(len(body)); got != want {
		t.Fatalf("TL bytes length at offset 12 = %#x, want %#x", got, want)
	}
	if !bytes.Equal(payload[13:13+len(body)], body) {
		t.Fatalf("tcp.query data body starts at offset 13, got %x want %x", payload[13:13+len(body)], body)
	}

	pkt, err := DecodeTCPPacket(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pkt.Data, body) {
		t.Fatalf("decoded data mismatch: got %x want %x", pkt.Data, body)
	}
}

func TestTCPQueryAnswerWire(t *testing.T) {
	w := NewWriter()
	w.WriteUint32(IDTCPQueryAnswer)
	w.WriteInt64(99)
	w.WriteBytes([]byte("answer"))
	pkt, err := DecodeTCPPacket(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Kind != TCPKindQueryAnswer {
		t.Errorf("kind: %v", pkt.Kind)
	}
	if pkt.ID != 99 {
		t.Errorf("id: %d", pkt.ID)
	}
	if string(pkt.Data) != "answer" {
		t.Errorf("data: %s", pkt.Data)
	}
}

func TestTCPQueryErrorWire(t *testing.T) {
	w := NewWriter()
	w.WriteUint32(IDTCPQueryError)
	w.WriteInt64(7)
	w.WriteInt32(13)
	w.WriteString("denied")
	pkt, err := DecodeTCPPacket(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Kind != TCPKindQueryError || pkt.ID != 7 || pkt.ErrCode != 13 || pkt.ErrMsg != "denied" {
		t.Errorf("decode: %+v", pkt)
	}
}

func TestTCPPingPongRoundTrip(t *testing.T) {
	pkt, err := DecodeTCPPacket(EncodeTCPPing(123))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Kind != TCPKindPing || pkt.ID != 123 {
		t.Errorf("ping: %+v", pkt)
	}
	pkt, err = DecodeTCPPacket(EncodeTCPPong(456))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Kind != TCPKindPong || pkt.ID != 456 {
		t.Errorf("pong: %+v", pkt)
	}
}

func TestTCPUnknownConstructor(t *testing.T) {
	w := NewWriter()
	w.WriteUint32(0xdeadbeef)
	pkt, err := DecodeTCPPacket(w.Bytes())
	if err == nil {
		t.Errorf("expected ErrUnknownConstructor, got pkt=%+v", pkt)
	}
}
