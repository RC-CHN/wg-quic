package quick

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

type localCoreEndpointControl struct {
	client control.Client
}

func (c localCoreEndpointControl) SetPeerEndpoint(ctx context.Context, update endpoint.PeerUpdate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.client.SetPeerEndpoint(control.SetPeerEndpointRequest{
		PublicKey: update.PublicKey, Endpoint: update.Endpoint.String(), Generation: update.Generation,
	})
}

func (c localCoreEndpointControl) ClearPeerEndpoint(
	ctx context.Context,
	publicKey string,
	generation uint64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.client.SetPeerEndpoint(control.SetPeerEndpointRequest{
		PublicKey: publicKey, Generation: generation,
	})
}

func (c localCoreEndpointControl) FinalizePeerEndpoint(
	ctx context.Context,
	publicKey string,
	generation uint64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.client.FinalizePeerEndpoint(publicKey, generation)
}

func (c localCoreEndpointControl) WaitPeerReady(ctx context.Context, update endpoint.PeerUpdate) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := c.client.Status()
		if err == nil {
			for _, peer := range status.Peers {
				if peer.PublicKey != update.PublicKey {
					continue
				}
				if peer.Generation > update.Generation {
					return fmt.Errorf(
						"peer endpoint generation advanced to %d while waiting for %d",
						peer.Generation, update.Generation,
					)
				}
				if peer.Generation == update.Generation &&
					peer.Endpoint == update.Endpoint.String() &&
					peer.AuthenticatedGeneration == update.Generation &&
					peer.AuthenticatedEndpoint == update.Endpoint.String() {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return errors.Join(ctx.Err(), err)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c localCoreEndpointControl) RedialPeer(ctx context.Context, publicKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.client.RedialPeer(publicKey)
}

func (c localCoreEndpointControl) Activate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.client.Activate()
}

func (c localCoreEndpointControl) PeerHealth(
	ctx context.Context,
	publicKey string,
) (endpoint.PeerHealth, error) {
	if err := ctx.Err(); err != nil {
		return endpoint.PeerHealth{}, err
	}
	status, err := c.client.Status()
	if err != nil {
		return endpoint.PeerHealth{}, err
	}
	for _, peer := range status.Peers {
		if peer.PublicKey == publicKey {
			return endpoint.PeerHealth{
				ConsecutiveReconnectFailures: peer.ConsecutiveReconnectFailures,
			}, nil
		}
	}
	return endpoint.PeerHealth{}, errors.New("peer public key is not configured")
}
