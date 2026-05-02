package cocoon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// streamChannel routes streaming `client.queryAnswerPart*` packets to a per-
// request Go channel.
type streamChannel struct {
	ch        chan Chunk
	done      chan struct{}
	sendMu    sync.Mutex
	closeOnce sync.Once
	close     func()
}

func (s *streamChannel) send(chunk Chunk) bool {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.ch <- chunk:
		return true
	case <-s.done:
		return false
	default:
		return false
	}
}

// dispatchPacket is the transport.onPacket callback. Streaming inference
// responses arrive as fire-and-forget tcp.packet payloads carrying
// client.queryAnswerPart{,Ex,Error,ErrorEx,EndPart} with the request_id field.
func (s *Session) dispatchPacket(payload []byte) {
	chunk, reqID, ok := tryDecodeQueryPart(payload)
	if !ok {
		return
	}
	s.mu.RLock()
	ch, hit := s.streams[reqID]
	s.mu.RUnlock()
	if !hit {
		return
	}
	_ = ch.send(chunk)
	if chunk.IsFinal {
		ch.close()
	}
}

// tryDecodeQueryPart parses a single chunk from a tcp.packet payload, if it
// matches a known streaming response constructor.
func tryDecodeQueryPart(payload []byte) (Chunk, [32]byte, bool) {
	r := tl.NewReader(payload)
	id, err := r.ReadUint32()
	if err != nil {
		return Chunk{}, [32]byte{}, false
	}

	var (
		out   Chunk
		reqID [32]byte
	)
	switch id {
	case tl.IDClientQueryAnswer, tl.IDClientQueryAnswerPart:
		// answer:bytes is_completed:Bool request_id:int256 request_tokens_used:tokensUsed
		body, err := r.ReadBytes()
		if err != nil {
			return out, reqID, false
		}
		isCompleted, err := r.ReadBool()
		if err != nil {
			return out, reqID, false
		}
		rid, err := r.ReadInt256()
		if err != nil {
			return out, reqID, false
		}
		toks, err := tl.DecodeTokensUsed(r)
		if err != nil {
			return out, reqID, false
		}
		return Chunk{
			Bytes:     body,
			IsFinal:   isCompleted,
			RequestID: rid,
			TokensUsed: TokensUsed{
				Prompt: toks.PromptTokens, Cached: toks.CachedTokens,
				Completion: toks.CompletionTokens, Reasoning: toks.ReasoningTokens,
				Total: toks.TotalTokens,
			},
		}, rid, true
	case tl.IDClientQueryAnswerEx, tl.IDClientQueryAnswerPartEx:
		// request_id:int256 answer:bytes flags:# final_info:flags.0?client.queryFinalInfo
		rid, err := r.ReadInt256()
		if err != nil {
			return out, reqID, false
		}
		body, err := r.ReadBytes()
		if err != nil {
			return out, reqID, false
		}
		flags, err := r.ReadFlags()
		if err != nil {
			return out, reqID, false
		}
		if flags&0x1 != 0 {
			_ = skipClientQueryFinalInfo(r)
		}
		if id == tl.IDClientQueryAnswerEx {
			if payload, ok := decodeHTTPResponsePayload(body); ok {
				body = payload
			}
		}
		return Chunk{
			Bytes:     body,
			IsFinal:   flags&0x1 != 0,
			RequestID: rid,
		}, rid, true
	case tl.IDClientQueryAnswerError, tl.IDClientQueryAnswerPartError:
		// error_code:int error:string request_id:int256 request_tokens_used:tokensUsed
		code, err := r.ReadInt32()
		if err != nil {
			return out, reqID, false
		}
		msg, err := r.ReadString()
		if err != nil {
			return out, reqID, false
		}
		rid, err := r.ReadInt256()
		if err != nil {
			return out, reqID, false
		}
		_, _ = tl.DecodeTokensUsed(r)
		return Chunk{
			IsFinal:   true,
			RequestID: rid,
			Err:       &ProxyError{Code: int(code), Message: msg, Phase: "query"},
		}, rid, true
	case tl.IDClientQueryAnswerErrorEx:
		// request_id:int256 error_code:int error:string flags:# final_info:flags.0?client.queryFinalInfo
		rid, err := r.ReadInt256()
		if err != nil {
			return out, reqID, false
		}
		code, err := r.ReadInt32()
		if err != nil {
			return out, reqID, false
		}
		msg, err := r.ReadString()
		if err != nil {
			return out, reqID, false
		}
		flags, err := r.ReadFlags()
		if err != nil {
			return out, reqID, false
		}
		if flags&0x1 != 0 {
			_ = skipClientQueryFinalInfo(r)
		}
		return Chunk{
			IsFinal:   true,
			RequestID: rid,
			Err:       &ProxyError{Code: int(code), Message: msg, Phase: "query"},
		}, rid, true
	}
	return Chunk{}, [32]byte{}, false
}

// RunQuery starts an inference request. Returns a channel of streaming chunks.
//
// Wire shape:
//
//	client.runQueryEx#f54cb74b model_name:string query:bytes max_coefficient:int
//	  max_tokens:int timeout:double request_id:int256 min_config_version:int
//	  flags:# enable_debug:flags.0?Bool public_key:flags.1?int256
//	  = client.QueryAnswerEx
//
// We always set flags.0 if EnableDebug is true; flags.1 is unused (we don't
// pass a public_key in v1).
func (s *Session) RunQuery(ctx context.Context, q Query) (<-chan Chunk, error) {
	if s.Status() != SessionReady {
		return nil, ErrNotConnected
	}
	if q.Model == "" {
		return nil, errors.New("cocoon: Query.Model required")
	}
	if q.Timeout <= 0 {
		q.Timeout = 5 * time.Minute
	}
	s.mu.RLock()
	tr := s.transport
	s.mu.RUnlock()
	if tr == nil {
		return nil, ErrNotConnected
	}

	var reqID [32]byte
	if _, err := rand.Read(reqID[:]); err != nil {
		return nil, fmt.Errorf("cocoon: rand: %w", err)
	}

	out := make(chan Chunk, 16)
	stream := &streamChannel{ch: out, done: make(chan struct{})}
	stream.close = func() {
		stream.closeOnce.Do(func() {
			s.mu.Lock()
			delete(s.streams, reqID)
			s.mu.Unlock()
			stream.sendMu.Lock()
			defer stream.sendMu.Unlock()
			close(stream.done)
			close(out)
		})
	}

	s.mu.Lock()
	s.streams[reqID] = stream
	s.mu.Unlock()

	body := encodeRunQueryEx(q, reqID, s.client.rootConfigVersion())
	if err := tr.SendPacket(body); err != nil {
		stream.close()
		return nil, err
	}

	// RunQueryEx is fire-and-forget at the tcp layer; answers are delivered as
	// tcp.packet messages keyed by request_id. Watch only lifecycle/timeouts.
	go func() {
		timer := time.NewTimer(q.Timeout)
		defer timer.Stop()
		sendFinal := func(chunk Chunk) {
			_ = stream.send(chunk)
			stream.close()
		}
		select {
		case <-ctx.Done():
			sendFinal(Chunk{IsFinal: true, RequestID: reqID, Err: ctx.Err()})
		case <-tr.Closed():
			sendFinal(Chunk{IsFinal: true, RequestID: reqID, Err: ErrNotConnected})
		case <-timer.C:
			sendFinal(Chunk{IsFinal: true, RequestID: reqID, Err: ErrRequestTimeout})
		}
	}()

	return out, nil
}

func encodeRunQueryEx(q Query, reqID [32]byte, minCfgVer int32) []byte {
	maxCoefficient := q.MaxCoefficient
	if maxCoefficient == 0 {
		maxCoefficient = 4000
	}
	maxTokens := q.MaxTokens
	if maxTokens == 0 {
		maxTokens = 10000
	}
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	requestBody := encodeHTTPRequest(q)

	w := tl.NewWriter()
	w.WriteUint32(tl.IDClientRunQueryEx)
	w.WriteString(q.Model)
	w.WriteBytes(requestBody)
	w.WriteInt32(int32(maxCoefficient))
	w.WriteInt32(int32(maxTokens))
	w.WriteDouble(timeout.Seconds() * 0.95)
	w.WriteInt256(reqID)
	w.WriteInt32(minCfgVer)
	flags := uint32(0)
	if q.EnableDebug {
		flags |= 0x1
	}
	w.WriteFlags(flags)
	if q.EnableDebug {
		w.WriteBool(true)
	}
	return w.Bytes()
}

func encodeHTTPRequest(q Query) []byte {
	path := q.Path
	if path == "" {
		path = "/v1/chat/completions"
	}
	headers := make(map[string]string, len(q.Headers)+2)
	for k, v := range q.Headers {
		if k != "" && v != "" {
			headers[k] = v
		}
	}
	if headers["Content-Type"] == "" {
		headers["Content-Type"] = "application/json"
	}
	headers["Content-Length"] = strconv.Itoa(len(q.Body))

	w := tl.NewWriter()
	w.WriteUint32(tl.IDHTTPRequest)
	w.WriteString("POST")
	w.WriteString(path)
	w.WriteString("HTTP/1.0")
	w.WriteVectorLen(len(headers))
	for k, v := range headers {
		w.WriteString(k)
		w.WriteString(v)
	}
	w.WriteBytes(q.Body)
	return w.Bytes()
}

func decodeHTTPResponsePayload(body []byte) ([]byte, bool) {
	r := tl.NewReader(body)
	id, err := r.ReadUint32()
	if err != nil || id != tl.IDHTTPResponse {
		return nil, false
	}
	if _, err = r.ReadString(); err != nil {
		return nil, false
	}
	if _, err = r.ReadInt32(); err != nil {
		return nil, false
	}
	if _, err = r.ReadString(); err != nil {
		return nil, false
	}
	n, err := r.ReadVectorLen()
	if err != nil {
		return nil, false
	}
	for i := 0; i < n; i++ {
		if _, err = r.ReadString(); err != nil {
			return nil, false
		}
		if _, err = r.ReadString(); err != nil {
			return nil, false
		}
	}
	payload, err := r.ReadBytes()
	if err != nil {
		return nil, false
	}
	return payload, true
}

func skipClientQueryFinalInfo(r *tl.Reader) error {
	flags, err := r.ReadFlags()
	if err != nil {
		return err
	}
	if _, err = tl.DecodeTokensUsed(r); err != nil {
		return err
	}
	if flags&0x1 != 0 {
		if _, err = r.ReadString(); err != nil {
			return err
		}
		if _, err = r.ReadString(); err != nil {
			return err
		}
	}
	if flags&0x2 != 0 {
		for i := 0; i < 4; i++ {
			if _, err = r.ReadDouble(); err != nil {
				return err
			}
		}
	}
	return nil
}
