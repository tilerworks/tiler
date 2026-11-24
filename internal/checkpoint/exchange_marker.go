package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const exchangeMarkerVersion = 2

// exchangeMarker identifies a checkpoint created by the exchange protocol.
type exchangeMarker struct {
	Version   int       `json:"version"`
	Protocol  string    `json:"protocol"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"created_at"`
}

func newExchangeMarker(target string, created time.Time) (exchangeMarker, error) {
	m := exchangeMarker{Version: exchangeMarkerVersion, Protocol: "exchange", Target: target, CreatedAt: created.UTC()}
	if err := m.validate(target); err != nil {
		return exchangeMarker{}, err
	}
	return m, nil
}

func (m exchangeMarker) validate(target string) error {
	if m.Version != exchangeMarkerVersion {
		return fmt.Errorf("unsupported checkpoint marker version %d", m.Version)
	}
	if m.Protocol != "exchange" {
		return errors.New("unsupported checkpoint marker protocol")
	}
	return (Marker{Version: markerVersion, Target: m.Target, CreatedAt: m.CreatedAt}).validate(target)
}

func marshalExchangeMarker(m exchangeMarker) ([]byte, error) {
	if err := m.validate(m.Target); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func parseExchangeMarker(data []byte, target string) (exchangeMarker, error) {
	var m exchangeMarker
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return exchangeMarker{}, fmt.Errorf("decode checkpoint marker: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return exchangeMarker{}, errors.New("checkpoint marker has trailing data")
	}
	if err := m.validate(target); err != nil {
		return exchangeMarker{}, err
	}
	return m, nil
}
