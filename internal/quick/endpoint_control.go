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
					peer.Session == "established" {
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
