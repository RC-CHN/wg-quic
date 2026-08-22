package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
	"github.com/RC-CHN/wg-quic/internal/wgdevice"
)

type corePeerTransactionState uint8

const (
	corePeerTransactionPrepared corePeerTransactionState = iota
	corePeerTransactionCommitted
	corePeerTransactionRolledBack
	corePeerTransactionFinalized
)

type corePeerTransaction struct {
	fingerprint [sha256.Size]byte
	prepared    wgdevice.PreparedPeerSet
	mutations   []wgdevice.PeerMutation
	added       map[string]*peerRuntime
	fecChanges  []corePeerFECChange
	fecApplied  bool
	state       corePeerTransactionState
}

type corePeerFECChange struct {
	publicKey string
	before    string
	after     string
}

func (i *Instance) preparePeerSet(request control.PeerSetRequest) error {
	if err := validateCoreTransactionID(request.TransactionID); err != nil {
		return err
	}
	if len(request.Mutations) == 0 {
		return errors.New("peer transaction requires at least one mutation")
	}
	encoded, err := json.Marshal(request.Mutations)
	if err != nil {
		return fmt.Errorf("fingerprint peer transaction: %w", err)
	}
	digest := hmac.New(sha256.New, i.peerFingerprintKey[:])
	_, _ = digest.Write(encoded)
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], digest.Sum(nil))

	i.peerTransactionMu.Lock()
	defer i.peerTransactionMu.Unlock()
	if existing := i.peerTransactions[request.TransactionID]; existing != nil {
		if existing.fingerprint != fingerprint {
			return errors.New("peer transaction ID was reused with different content")
		}
		return nil
	}
	if i.activePeerTransaction != "" {
		return errors.New("another peer transaction is active")
	}
	mutations, err := parseCorePeerMutations(request.Mutations)
	if err != nil {
		return err
	}
	prepared, err := wgdevice.PreparePeerSet(i.device, mutations)
	if err != nil {
		return fmt.Errorf("prepare WireGuard peer set: %w", err)
	}
	transaction := &corePeerTransaction{
		fingerprint: fingerprint, prepared: prepared, mutations: mutations,
		added: make(map[string]*peerRuntime), state: corePeerTransactionPrepared,
	}
	if err := i.preparePeerTransportKeys(transaction); err != nil {
		_ = prepared.Rollback()
		transaction.releasePreparedAdditions()
		return err
	}
	i.peerTransactions[request.TransactionID] = transaction
	i.peerTransactionOrder = append(i.peerTransactionOrder, request.TransactionID)
	i.activePeerTransaction = request.TransactionID
	i.trimPeerTransactionsLocked()
	return nil
}

func parseCorePeerMutations(values []control.PeerMutation) ([]wgdevice.PeerMutation, error) {
	result := make([]wgdevice.PeerMutation, len(values))
	for index, value := range values {
		operation := wgdevice.PeerMutationOperation(value.Operation)
		switch operation {
		case wgdevice.PeerMutationAdd, wgdevice.PeerMutationUpdate, wgdevice.PeerMutationRemove:
		default:
			return nil, fmt.Errorf("peer mutation %d has unsupported operation %q", index+1, value.Operation)
		}
		peer := config.Peer{
			PublicKey: value.PublicKey, PresharedKey: value.PresharedKey,
			Endpoint: value.Endpoint, PersistentKeepalive: value.PersistentKeepalive,
			FECPolicy: value.FECPolicy,
		}
		for prefixIndex, text := range value.AllowedIPs {
			prefix, err := netip.ParsePrefix(text)
			if err != nil {
				return nil, fmt.Errorf("peer mutation %d AllowedIP %d: %w", index+1, prefixIndex+1, err)
			}
			peer.AllowedIPs = append(peer.AllowedIPs, prefix.Masked())
		}
		result[index] = wgdevice.PeerMutation{Operation: operation, Peer: peer}
	}
	return result, nil
}

func (i *Instance) preparePeerTransportKeys(transaction *corePeerTransaction) error {
	for _, mutation := range transaction.mutations {
		policy := canonicalPeerFECPolicy(mutation.Peer.FECPolicy)
		if mutation.Peer.FECPolicy != "" && policy != mutation.Peer.FECPolicy {
			return fmt.Errorf("peer %s has unsupported FEC policy %q", mutation.Peer.PublicKey, mutation.Peer.FECPolicy)
		}
		if mutation.Operation == wgdevice.PeerMutationAdd {
			transaction.added[mutation.Peer.PublicKey] = &peerRuntime{
				status:    control.PeerStatus{PublicKey: mutation.Peer.PublicKey},
				fecPolicy: policy,
			}
		} else if mutation.Operation == wgdevice.PeerMutationUpdate {
			i.endpointMu.RLock()
			runtime := i.peers[mutation.Peer.PublicKey]
			if runtime != nil && runtime.fecPolicy != policy {
				transaction.fecChanges = append(transaction.fecChanges, corePeerFECChange{
					publicKey: mutation.Peer.PublicKey,
					before:    runtime.fecPolicy, after: policy,
				})
			}
			i.endpointMu.RUnlock()
		}
	}
	if i.cfg.Transport.Obfs == "none" {
		return nil
	}
	localPrivate, err := decodeWireGuardKey(i.cfg.Interface.PrivateKey)
	if err != nil {
		return fmt.Errorf("decode local WireGuard private key: %w", err)
	}
	for _, mutation := range transaction.mutations {
		if mutation.Operation != wgdevice.PeerMutationAdd {
			continue
		}
		key, err := deriveTransportPeerKey(localPrivate, mutation.Peer)
		if err != nil {
			return fmt.Errorf("derive added peer obfuscation key: %w", err)
		}
		runtime := transaction.added[mutation.Peer.PublicKey]
		runtime.obfsKey = key
		runtime.hasObfsKey = true
		runtime.releaseReceiveKey = i.bind.AcquireReceiveKey(key)
	}
	return nil
}

func (i *Instance) commitPeerSet(transactionID string) error {
	i.peerTransactionMu.Lock()
	defer i.peerTransactionMu.Unlock()
	transaction, err := i.lookupPeerTransactionLocked(transactionID)
	if err != nil {
		return err
	}
	switch transaction.state {
	case corePeerTransactionCommitted, corePeerTransactionFinalized:
		return nil
	case corePeerTransactionRolledBack:
		return errors.New("peer transaction was rolled back")
	}
	if err := i.applyPeerFECChanges(transaction); err != nil {
		return fmt.Errorf("apply peer FEC policy: %w", err)
	}
	if err := transaction.prepared.Commit(); err != nil {
		rollbackErr := i.rollbackPeerFECChanges(transaction)
		if rollbackErr != nil {
			return errors.Join(
				fmt.Errorf("commit WireGuard peer set: %w", err),
				fmt.Errorf("rollback peer FEC policy: %w", rollbackErr),
			)
		}
		return fmt.Errorf("commit WireGuard peer set: %w", err)
	}
	i.endpointMu.Lock()
	for _, mutation := range transaction.mutations {
		if mutation.Operation != wgdevice.PeerMutationAdd {
			continue
		}
		i.peers[mutation.Peer.PublicKey] = transaction.added[mutation.Peer.PublicKey]
		i.peerOrder = append(i.peerOrder, mutation.Peer.PublicKey)
	}
	i.endpointMu.Unlock()
	transaction.state = corePeerTransactionCommitted
	return nil
}

func (i *Instance) rollbackPeerSet(transactionID string) error {
	i.peerTransactionMu.Lock()
	defer i.peerTransactionMu.Unlock()
	transaction, err := i.lookupPeerTransactionLocked(transactionID)
	if err != nil {
		return err
	}
	switch transaction.state {
	case corePeerTransactionRolledBack:
		return nil
	case corePeerTransactionFinalized:
		return errors.New("finalized peer transaction cannot be rolled back")
	}
	committed := transaction.state == corePeerTransactionCommitted
	if transaction.fecApplied {
		if err := i.rollbackPeerFECChanges(transaction); err != nil {
			return fmt.Errorf("rollback peer FEC policy: %w", err)
		}
	}
	if err := transaction.prepared.Rollback(); err != nil {
		return fmt.Errorf("rollback WireGuard peer set: %w", err)
	}
	if committed {
		i.endpointMu.Lock()
		for _, mutation := range transaction.mutations {
			if mutation.Operation != wgdevice.PeerMutationAdd {
				continue
			}
			runtime := i.peers[mutation.Peer.PublicKey]
			if runtime != nil {
				i.releasePeerRuntimeLocked(runtime)
			}
			delete(i.peers, mutation.Peer.PublicKey)
			i.peerOrder = slices.DeleteFunc(i.peerOrder, func(value string) bool {
				return value == mutation.Peer.PublicKey
			})
		}
		i.endpointMu.Unlock()
	} else {
		transaction.releasePreparedAdditions()
	}
	transaction.state = corePeerTransactionRolledBack
	transaction.clearSecrets()
	if i.activePeerTransaction == transactionID {
		i.activePeerTransaction = ""
	}
	return nil
}

func (i *Instance) applyPeerFECChanges(transaction *corePeerTransaction) error {
	if transaction.fecApplied || len(transaction.fecChanges) == 0 {
		transaction.fecApplied = true
		return nil
	}
	i.endpointMu.Lock()
	defer i.endpointMu.Unlock()
	applied := 0
	for index := range transaction.fecChanges {
		change := &transaction.fecChanges[index]
		runtime := i.peers[change.publicKey]
		if runtime == nil || runtime.fecPolicy != change.before {
			err := errors.New("peer FEC policy changed after transaction preparation")
			return errors.Join(err, i.rollbackPeerFECChangesLocked(transaction, applied))
		}
		endpoint, endpointErr := peerendpoint.ParseNumeric(runtime.status.SelectedEndpoint)
		if endpointErr == nil {
			if runtime.releaseFECPolicy == nil {
				err := errors.New("active endpoint is missing its FEC policy lease")
				return errors.Join(err, i.rollbackPeerFECChangesLocked(transaction, applied))
			}
			release, err := i.bind.ReplaceEndpointFECPolicy(endpoint, change.before, change.after)
			if err != nil {
				return errors.Join(err, i.rollbackPeerFECChangesLocked(transaction, applied))
			}
			runtime.releaseFECPolicy = release
		}
		runtime.fecPolicy = change.after
		applied++
	}
	transaction.fecApplied = true
	return nil
}

func (i *Instance) rollbackPeerFECChanges(transaction *corePeerTransaction) error {
	if !transaction.fecApplied {
		return nil
	}
	i.endpointMu.Lock()
	defer i.endpointMu.Unlock()
	err := i.rollbackPeerFECChangesLocked(transaction, len(transaction.fecChanges))
	if err == nil {
		transaction.fecApplied = false
	}
	return err
}

func (i *Instance) rollbackPeerFECChangesLocked(
	transaction *corePeerTransaction,
	applied int,
) error {
	var rollbackErrors []error
	for index := applied - 1; index >= 0; index-- {
		change := &transaction.fecChanges[index]
		runtime := i.peers[change.publicKey]
		if runtime == nil || runtime.fecPolicy != change.after {
			rollbackErrors = append(rollbackErrors, fmt.Errorf(
				"peer %s FEC policy changed during rollback", change.publicKey,
			))
			continue
		}
		endpoint, endpointErr := peerendpoint.ParseNumeric(runtime.status.SelectedEndpoint)
		if endpointErr == nil {
			release, err := i.bind.ReplaceEndpointFECPolicy(endpoint, change.after, change.before)
			if err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"restore peer %s FEC policy: %w", change.publicKey, err,
				))
				continue
			}
			runtime.releaseFECPolicy = release
		}
		runtime.fecPolicy = change.before
	}
	return errors.Join(rollbackErrors...)
}

func (i *Instance) finalizePeerSet(transactionID string) error {
	i.peerTransactionMu.Lock()
	defer i.peerTransactionMu.Unlock()
	transaction, err := i.lookupPeerTransactionLocked(transactionID)
	if err != nil {
		return err
	}
	switch transaction.state {
	case corePeerTransactionFinalized:
		return nil
	case corePeerTransactionPrepared:
		return errors.New("uncommitted peer transaction cannot be finalized")
	case corePeerTransactionRolledBack:
		return errors.New("rolled-back peer transaction cannot be finalized")
	}
	if err := transaction.prepared.Finalize(); err != nil {
		return fmt.Errorf("finalize WireGuard peer set: %w", err)
	}
	i.endpointMu.Lock()
	for _, mutation := range transaction.mutations {
		if mutation.Operation != wgdevice.PeerMutationRemove {
			continue
		}
		runtime := i.peers[mutation.Peer.PublicKey]
		if runtime != nil {
			i.releasePeerRuntimeLocked(runtime)
		}
		delete(i.peers, mutation.Peer.PublicKey)
		i.peerOrder = slices.DeleteFunc(i.peerOrder, func(value string) bool {
			return value == mutation.Peer.PublicKey
		})
	}
	i.endpointMu.Unlock()
	i.applyCommittedPeerConfig(transaction.mutations)
	transaction.state = corePeerTransactionFinalized
	transaction.clearSecrets()
	if i.activePeerTransaction == transactionID {
		i.activePeerTransaction = ""
	}
	return nil
}

func (i *Instance) lookupPeerTransactionLocked(transactionID string) (*corePeerTransaction, error) {
	if err := validateCoreTransactionID(transactionID); err != nil {
		return nil, err
	}
	transaction := i.peerTransactions[transactionID]
	if transaction == nil {
		return nil, errors.New("peer transaction ID is unknown")
	}
	return transaction, nil
}

func validateCoreTransactionID(value string) error {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("peer transaction ID must contain 1 to 128 safe bytes")
	}
	return nil
}

func (i *Instance) releasePeerRuntimeLocked(runtime *peerRuntime) {
	if endpoint, err := peerendpoint.ParseNumeric(runtime.status.SelectedEndpoint); err == nil {
		i.bind.RetireEndpoint(endpoint)
	}
	for _, resource := range runtime.obsoleteEndpoints {
		if resource.endpoint.IsValid() {
			i.bind.RetireEndpoint(resource.endpoint)
		}
		if resource.releaseAssociation != nil {
			resource.releaseAssociation()
		}
		if resource.releaseFECPolicy != nil {
			resource.releaseFECPolicy()
		}
	}
	runtime.obsoleteEndpoints = nil
	if runtime.releaseFECPolicy != nil {
		runtime.releaseFECPolicy()
		runtime.releaseFECPolicy = nil
	}
	if runtime.releaseAssociation != nil {
		runtime.releaseAssociation()
		runtime.releaseAssociation = nil
	}
	if runtime.releaseReceiveKey != nil {
		runtime.releaseReceiveKey()
		runtime.releaseReceiveKey = nil
	}
}

func (transaction *corePeerTransaction) releasePreparedAdditions() {
	for _, runtime := range transaction.added {
		if runtime.releaseReceiveKey != nil {
			runtime.releaseReceiveKey()
			runtime.releaseReceiveKey = nil
		}
	}
}

func (transaction *corePeerTransaction) clearSecrets() {
	for index := range transaction.mutations {
		transaction.mutations[index].Peer.PresharedKey = ""
	}
}

func (i *Instance) applyCommittedPeerConfig(mutations []wgdevice.PeerMutation) {
	i.mu.Lock()
	defer i.mu.Unlock()
	next := i.cfg.Clone()
	for _, mutation := range mutations {
		index := slices.IndexFunc(next.Peers, func(peer config.Peer) bool {
			return peer.PublicKey == mutation.Peer.PublicKey
		})
		switch mutation.Operation {
		case wgdevice.PeerMutationAdd:
			next.Peers = append(next.Peers, mutation.Peer)
		case wgdevice.PeerMutationUpdate:
			if index >= 0 {
				next.Peers[index] = mutation.Peer
			}
		case wgdevice.PeerMutationRemove:
			if index >= 0 {
				next.Peers = slices.Delete(next.Peers, index, index+1)
			}
		}
	}
	i.cfg = next
}

func (i *Instance) trimPeerTransactionsLocked() {
	const retainedTransactions = 256
	for len(i.peerTransactionOrder) > retainedTransactions {
		transactionID := i.peerTransactionOrder[0]
		transaction := i.peerTransactions[transactionID]
		if transaction != nil && (transaction.state == corePeerTransactionPrepared ||
			transaction.state == corePeerTransactionCommitted) {
			return
		}
		delete(i.peerTransactions, transactionID)
		i.peerTransactionOrder = i.peerTransactionOrder[1:]
	}
}

func (i *Instance) closePreparedPeerTransactions() {
	i.peerTransactionMu.Lock()
	defer i.peerTransactionMu.Unlock()
	for _, transaction := range i.peerTransactions {
		if transaction.state != corePeerTransactionPrepared {
			continue
		}
		_ = transaction.prepared.Rollback()
		transaction.releasePreparedAdditions()
		transaction.state = corePeerTransactionRolledBack
		transaction.clearSecrets()
	}
}
