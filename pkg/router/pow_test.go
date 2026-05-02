package router

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestPoWSolveAndVerify(t *testing.T) {
	var salt [16]byte
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	const difficulty = 12 // 12 leading zero bits = ~ms to solve

	nonce, err := findNonce(salt, difficulty)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPoWResponse(salt, difficulty, nonce) {
		t.Errorf("VerifyPoWResponse(found nonce) returned false")
	}
	if VerifyPoWResponse(salt, difficulty+8, nonce) {
		t.Errorf("higher difficulty should reject")
	}
}

func TestPoWFindNonceZeroDifficulty(t *testing.T) {
	var salt [16]byte
	got, err := findNonce(salt, 0)
	if err != nil || got != 0 {
		t.Errorf("zero-difficulty: nonce=%d err=%v", got, err)
	}
}

// TestPoWEndToEnd boots a fake server that performs the PoW protocol against
// our SolvePoW client.
func TestPoWEndToEnd(t *testing.T) {
	const difficulty = 10

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// Server side: send challenge, read response, verify.
	serverDone := make(chan error, 1)
	go func() {
		var salt [16]byte
		for i := range salt {
			salt[i] = byte(i ^ 0x55)
		}
		var ch [24]byte
		binary.LittleEndian.PutUint32(ch[0:4], 0x418e1291)
		binary.LittleEndian.PutUint32(ch[4:8], uint32(difficulty))
		copy(ch[8:24], salt[:])
		if err := a.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverDone <- err
			return
		}
		if _, err := a.Write(ch[:]); err != nil {
			serverDone <- err
			return
		}
		var resp [12]byte
		if _, err := readFull(a, resp[:]); err != nil {
			serverDone <- err
			return
		}
		magic := binary.LittleEndian.Uint32(resp[0:4])
		if magic != 0x01827319 {
			serverDone <- errors.New("bad response magic")
			return
		}
		nonce := int64(binary.LittleEndian.Uint64(resp[4:12]))
		if !VerifyPoWResponse(salt, difficulty, nonce) {
			serverDone <- errors.New("server-side verification failed")
			return
		}
		serverDone <- nil
	}()

	if err := b.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := SolvePoW(b, MaxDifficulty); err != nil {
		t.Fatalf("SolvePoW: %v", err)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestPoWMagicMismatch(t *testing.T) {
	bad := bytes.NewReader([]byte{
		0xff, 0xff, 0xff, 0xff, // bad magic
		0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	})
	rw := &readWriter{r: bad, w: &bytes.Buffer{}}
	err := SolvePoW(rw, MaxDifficulty)
	if !errors.Is(err, ErrPoWMagicMismatch) {
		t.Errorf("got %v, want ErrPoWMagicMismatch", err)
	}
}

func TestPoWDifficultyTooHigh(t *testing.T) {
	var b [24]byte
	binary.LittleEndian.PutUint32(b[0:4], 0x418e1291)
	binary.LittleEndian.PutUint32(b[4:8], 64) // exceed MaxDifficulty
	rw := &readWriter{r: bytes.NewReader(b[:]), w: &bytes.Buffer{}}
	err := SolvePoW(rw, MaxDifficulty)
	if !errors.Is(err, ErrPoWDifficultyTooHigh) {
		t.Errorf("got %v, want ErrPoWDifficultyTooHigh", err)
	}
}

// readWriter pairs an io.Reader and io.Writer to satisfy io.ReadWriter.
type readWriter struct {
	r io.Reader
	w io.Writer
}

func (rw *readWriter) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw *readWriter) Write(p []byte) (int, error) { return rw.w.Write(p) }

// readFull reads exactly len(b) bytes or returns an error.
func readFull(r net.Conn, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := r.Read(b[got:])
		if err != nil {
			return got, err
		}
		got += n
	}
	return got, nil
}
