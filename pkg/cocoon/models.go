package cocoon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// WorkerTypes asks the connected proxy for its current live worker models.
func (s *Session) WorkerTypes(ctx context.Context) ([]WorkerType, error) {
	if s.Status() != SessionReady {
		return nil, ErrNotConnected
	}
	s.mu.RLock()
	tr := s.transport
	s.mu.RUnlock()
	if tr == nil {
		return nil, ErrNotConnected
	}

	w := tl.NewWriter()
	w.WriteUint32(tl.IDClientGetWorkerTypesV2)
	_, answer, errCh, err := tr.SendQuery(w.Bytes(), 10*time.Second)
	if err != nil {
		return nil, err
	}
	select {
	case payload := <-answer:
		return decodeWorkerTypesV2(payload)
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func decodeWorkerTypesV2(payload []byte) ([]WorkerType, error) {
	r := tl.NewReader(payload)
	id, err := r.ReadUint32()
	if err != nil {
		return nil, err
	}
	if id != tl.IDClientWorkerTypesV2 {
		return nil, fmt.Errorf("cocoon: workerTypesV2: unexpected constructor %#x", id)
	}
	n, err := r.ReadVectorLen()
	if err != nil {
		return nil, err
	}
	out := make([]WorkerType, 0, n)
	for i := 0; i < n; i++ {
		name, err := r.ReadString()
		if err != nil {
			return nil, err
		}
		workerCount, err := r.ReadVectorLen()
		if err != nil {
			return nil, err
		}
		workers := make([]WorkerInstance, 0, workerCount)
		for j := 0; j < workerCount; j++ {
			if _, err = r.ReadFlags(); err != nil {
				return nil, err
			}
			coefficient, err := r.ReadInt32()
			if err != nil {
				return nil, err
			}
			active, err := r.ReadInt32()
			if err != nil {
				return nil, err
			}
			maxActive, err := r.ReadInt32()
			if err != nil {
				return nil, err
			}
			workers = append(workers, WorkerInstance{
				Coefficient:       coefficient,
				ActiveRequests:    active,
				MaxActiveRequests: maxActive,
			})
		}
		out = append(out, WorkerType{Name: name, Workers: workers})
	}
	if len(out) == 0 {
		return nil, errors.New("cocoon: proxy has no advertised worker models")
	}
	return out, nil
}
