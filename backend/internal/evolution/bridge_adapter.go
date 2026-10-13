package evolution

import (
	"context"
	"errors"
	"fmt"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

// BridgeAdapter exposes a *Client as the bridge.Evolution consumer-side port
// (ARCHITECTURE §2 rule 3). The engine declares the port for the E9 call-reply
// path; it is not consumed by bridge.Deps yet.
type BridgeAdapter struct {
	client *Client
}

// BridgeAdapter implements the bridge.Evolution port.
var _ bridge.Evolution = (*BridgeAdapter)(nil)

// NewBridgeAdapter wraps client as the bridge.Evolution port. A nil client is a
// wiring bug and panics.
func NewBridgeAdapter(client *Client) *BridgeAdapter {
	if client == nil {
		panic("evolution: NewBridgeAdapter requires a non-nil client")
	}
	return &BridgeAdapter{client: client}
}

// SendText sends a text message. An unknown send outcome is mapped to the
// bridge-owned bridge.ErrUnconfirmed so the engine quarantines it and never
// replays it (ADR-0008 §5); a pre-flight rejection (known not sent) is returned
// unchanged.
func (a *BridgeAdapter) SendText(ctx context.Context, number, text string) (*bridge.SendResult, error) {
	result, err := a.client.SendText(ctx, number, text, SendOptions{})
	if err != nil {
		if errors.Is(err, ErrUnconfirmedSend) {
			return nil, fmt.Errorf("%w: %w", bridge.ErrUnconfirmed, err)
		}
		return nil, err
	}
	return &bridge.SendResult{MessageID: result.MessageID}, nil
}
