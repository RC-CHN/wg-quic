//go:build windows

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/RC-CHN/wg-quic/internal/config"
)

const windowsPeerRouteJournalPrefix = "peer-routes-v1-"

type windowsPeerRouteJournal struct {
	mu            sync.Mutex
	tunnel        string
	interfaceLUID uint64
	compartmentID uint32
	path          string
	store         *windowsDiskRouteStore
	system        windowsRouteSystem
	record        *peerRouteJournal
	recovery      RecoveryStatus
}

func newWindowsPeerRouteManager(
	ctx context.Context,
	name string,
	cfg *config.Config,
) (PeerRouteManager, error) {
	if cfg == nil {
		return nil, errors.New("Windows peer route configuration is required")
	}
	table := strings.ToLower(strings.TrimSpace(cfg.Interface.Table))
	if table != "" && table != "auto" && table != "off" {
		return nil, fmt.Errorf("Windows does not support explicit route table %q", cfg.Interface.Table)
	}
	luid, err := windowsInterfaceLUID(name)
	if err != nil {
		return nil, err
	}
	system := windowsNativeRouteSystem{}
	journal, err := newWindowsPeerRouteJournal(name, luid, system)
	if err != nil {
		return nil, err
	}
	initial := []netip.Prefix(nil)
	if table != "off" {
		initial, err = canonicalRoutePlanPrefixes(uniqueAllowedPrefixes(cfg))
		if err != nil {
			return nil, err
		}
	}
	if err := journal.recover(ctx, initial); err != nil {
		return nil, fmt.Errorf("recover Windows peer routes: %w", err)
	}
	inner, err := newCommandPeerRouteManager(
		cfg,
		func(prefix netip.Prefix) ([]hostOperation, error) {
			key, err := windowsPeerRouteKey(system.CurrentCompartmentID(), luid, prefix)
			if err != nil {
				return nil, err
			}
			return []hostOperation{{
				apply: windowsPeerRouteCommand("add", key),
				undo:  windowsPeerRouteCommand("delete", key),
			}}, nil
		},
		func(runCtx context.Context, command hostCommand) error {
			key, action, err := parseWindowsPeerRouteCommand(command)
			if err != nil {
				return err
			}
			switch action {
			case "add":
				return system.CreateRoute(runCtx, windowsSelectedRoute{Key: key})
			case "delete":
				return system.DeleteRoute(runCtx, key)
			default:
				return errors.New("invalid Windows peer route action")
			}
		},
	)
	if err != nil {
		return nil, err
	}
	if table == "off" {
		return inner, nil
	}
	return newJournaledPeerRouteManager(inner, journal, initial)
}

func windowsPeerRouteKey(
	compartmentID uint32,
	interfaceLUID uint64,
	prefix netip.Prefix,
) (windowsRouteKey, error) {
	if !prefix.IsValid() {
		return windowsRouteKey{}, errors.New("Windows peer route prefix is invalid")
	}
	prefix = prefix.Masked()
	family, nextHop := uint8(6), "::"
	if prefix.Addr().Is4() {
		family, nextHop = 4, "0.0.0.0"
	}
	return windowsRouteKey{
		CompartmentID: compartmentID,
		Family:        family, Destination: prefix.String(),
		InterfaceLUID: interfaceLUID, NextHop: nextHop,
	}, nil
}

func windowsPeerRouteCommand(action string, key windowsRouteKey) hostCommand {
	return hostCommand{name: "windows-peer-route-" + action, args: []string{
		fmt.Sprint(key.CompartmentID), fmt.Sprint(key.InterfaceLUID),
		key.Destination, key.NextHop,
	}}
}

func parseWindowsPeerRouteCommand(command hostCommand) (windowsRouteKey, string, error) {
	const prefix = "windows-peer-route-"
	action := strings.TrimPrefix(command.name, prefix)
	if action == command.name || len(command.args) != 4 {
		return windowsRouteKey{}, "", errors.New("invalid Windows peer route command")
	}
	var compartmentID uint32
	var interfaceLUID uint64
	if _, err := fmt.Sscan(command.args[0], &compartmentID); err != nil {
		return windowsRouteKey{}, "", err
	}
	if _, err := fmt.Sscan(command.args[1], &interfaceLUID); err != nil {
		return windowsRouteKey{}, "", err
	}
	prefixValue, err := netip.ParsePrefix(command.args[2])
	if err != nil {
		return windowsRouteKey{}, "", err
	}
	key, err := windowsPeerRouteKey(compartmentID, interfaceLUID, prefixValue)
	if err != nil {
		return windowsRouteKey{}, "", err
	}
	if key.NextHop != command.args[3] {
		return windowsRouteKey{}, "", errors.New("Windows peer route next hop is not canonical")
	}
	return key, action, nil
}

func newWindowsPeerRouteJournal(
	tunnel string,
	interfaceLUID uint64,
	system windowsRouteSystem,
) (*windowsPeerRouteJournal, error) {
	store := newWindowsDiskRouteStore()
	if err := store.prepare(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(tunnel))
	filename := windowsPeerRouteJournalPrefix + hex.EncodeToString(sum[:12]) + ".json"
	return &windowsPeerRouteJournal{
		tunnel: tunnel, interfaceLUID: interfaceLUID,
		compartmentID: system.CurrentCompartmentID(),
		path:          filepath.Join(store.stateDirectory, filename),
		store:         store, system: system,
		recovery: RecoveryStatus{State: "clean"},
	}, nil
}

func (j *windowsPeerRouteJournal) Begin(
	ctx context.Context,
	transactionID string,
	before, after []netip.Prefix,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record := peerRouteJournal{
		SchemaVersion: peerRouteJournalSchemaVersion,
		Tunnel:        j.tunnel, TransactionID: transactionID,
		InterfaceLUID: j.interfaceLUID, CompartmentID: j.compartmentID,
		Phase:  peerRouteJournalPrepared,
		Before: peerRoutePrefixStrings(before), After: peerRoutePrefixStrings(after),
	}
	if j.record != nil {
		record.Generation = j.record.Generation
	}
	return j.saveLocked(ctx, &record)
}

func (j *windowsPeerRouteJournal) Mark(
	ctx context.Context,
	phase peerRouteJournalPhase,
	removalsApplied, additionsApplied bool,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.record == nil {
		return errors.New("Windows peer route journal has no active transaction")
	}
	record := *j.record
	record.Phase = phase
	record.RemovalsApplied = removalsApplied
	record.AdditionsApplied = additionsApplied
	return j.saveLocked(ctx, &record)
}

func (j *windowsPeerRouteJournal) Active(
	ctx context.Context,
	prefixes []netip.Prefix,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record := peerRouteJournal{
		SchemaVersion: peerRouteJournalSchemaVersion,
		Tunnel:        j.tunnel, InterfaceLUID: j.interfaceLUID,
		CompartmentID: j.compartmentID, Phase: peerRouteJournalActive,
		Before: peerRoutePrefixStrings(prefixes), After: peerRoutePrefixStrings(prefixes),
	}
	if j.record != nil {
		record.Generation = j.record.Generation
	}
	return j.saveLocked(ctx, &record)
}

func (j *windowsPeerRouteJournal) Cleanup(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recoverLocked(ctx, nil, false)
}

func (j *windowsPeerRouteJournal) RecoveryStatus() RecoveryStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recovery
}

func (j *windowsPeerRouteJournal) forceCleanup(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recoverLocked(ctx, nil, true)
}

func (j *windowsPeerRouteJournal) recover(
	ctx context.Context,
	canonical []netip.Prefix,
) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recoverLocked(ctx, canonical, false)
}

func (j *windowsPeerRouteJournal) recoverLocked(
	ctx context.Context,
	canonical []netip.Prefix,
	forceAmbiguous bool,
) error {
	record, err := j.loadLocked(ctx)
	if errors.Is(err, os.ErrNotExist) {
		j.record = nil
		return nil
	}
	if err != nil {
		return err
	}
	if record.Tunnel != j.tunnel || record.InterfaceLUID != j.interfaceLUID ||
		record.CompartmentID != j.compartmentID {
		log.Printf(
			"wg-quic: retaining Windows peer routes from journal %s because its tunnel identity no longer matches",
			j.path,
		)
		j.recovery.State = "degraded"
		retained := make(map[string]struct{}, len(record.Before)+len(record.After))
		for _, prefix := range append(slices.Clone(record.Before), record.After...) {
			retained[prefix] = struct{}{}
		}
		j.recovery.RetainedAmbiguousObjects += len(retained)
		j.recovery.Message = "retained peer routes whose recorded Windows tunnel identity no longer matches"
		return j.quarantineLocked(ctx, "identity-mismatch")
	}

	provenSet := make(map[netip.Prefix]struct{}, len(record.Before)+len(record.After))
	for _, prefix := range peerRouteJournalPrefixes(record.Before) {
		provenSet[prefix] = struct{}{}
	}
	if record.AdditionsApplied || record.Phase == peerRouteJournalCommitted {
		for _, prefix := range peerRouteJournalPrefixes(record.After) {
			provenSet[prefix] = struct{}{}
		}
	}
	canonicalSet := make(map[netip.Prefix]struct{}, len(canonical))
	for _, prefix := range canonical {
		canonicalSet[prefix.Masked()] = struct{}{}
	}
	beforeSet := make(map[netip.Prefix]struct{}, len(record.Before))
	for _, prefix := range peerRouteJournalPrefixes(record.Before) {
		beforeSet[prefix] = struct{}{}
	}
	for _, prefix := range peerRouteJournalPrefixes(record.After) {
		if _, wasOwned := beforeSet[prefix]; wasOwned || record.AdditionsApplied ||
			record.Phase == peerRouteJournalCommitted {
			continue
		}
		if _, adopted := canonicalSet[prefix]; adopted || forceAmbiguous {
			provenSet[prefix] = struct{}{}
			continue
		}
		key, keyErr := windowsPeerRouteKey(j.compartmentID, j.interfaceLUID, prefix)
		if keyErr != nil {
			return keyErr
		}
		exists, existsErr := j.system.RouteExists(ctx, key)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			log.Printf(
				"wg-quic: retaining ambiguous Windows peer route %s left by interrupted add",
				prefix,
			)
			j.recovery.State = "degraded"
			j.recovery.RetainedAmbiguousObjects++
			j.recovery.Message = "retained ambiguous Windows peer routes left by an interrupted add"
		}
	}
	proven := make([]netip.Prefix, 0, len(provenSet))
	for prefix := range provenSet {
		proven = append(proven, prefix)
	}
	proven, err = canonicalRoutePlanPrefixes(proven)
	if err != nil {
		return err
	}
	for _, prefix := range proven {
		key, keyErr := windowsPeerRouteKey(j.compartmentID, j.interfaceLUID, prefix)
		if keyErr != nil {
			return keyErr
		}
		exists, existsErr := j.system.RouteExists(ctx, key)
		if existsErr != nil {
			return fmt.Errorf("inspect recorded Windows peer route %s: %w", prefix, existsErr)
		}
		if exists {
			if deleteErr := j.system.DeleteRoute(ctx, key); deleteErr != nil {
				return fmt.Errorf("remove recorded Windows peer route %s: %w", prefix, deleteErr)
			}
		}
	}
	return j.removeLocked(ctx)
}

func (j *windowsPeerRouteJournal) saveLocked(
	ctx context.Context,
	record *peerRouteJournal,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := j.store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	record.Generation++
	encoded, err := encodePeerRouteJournal(*record)
	if err != nil {
		return err
	}
	if err := j.store.atomicWrite(j.path, encoded); err != nil {
		return fmt.Errorf("write Windows peer route journal: %w", err)
	}
	decoded, err := decodePeerRouteJournal(encoded)
	if err != nil {
		return err
	}
	j.record = &decoded
	return nil
}

func (j *windowsPeerRouteJournal) loadLocked(ctx context.Context) (peerRouteJournal, error) {
	if err := ctx.Err(); err != nil {
		return peerRouteJournal{}, err
	}
	unlock, err := j.store.Lock(ctx)
	if err != nil {
		return peerRouteJournal{}, err
	}
	defer func() { _ = unlock() }()
	file, err := os.Open(j.path)
	if err != nil {
		return peerRouteJournal{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPeerRouteJournalSize+1))
	if err != nil {
		return peerRouteJournal{}, err
	}
	record, err := decodePeerRouteJournal(data)
	if err != nil {
		if quarantineErr := quarantineWindowsRouteLedger(j.path); quarantineErr != nil {
			return peerRouteJournal{}, errors.Join(err, quarantineErr)
		}
		return peerRouteJournal{}, fmt.Errorf("quarantined corrupt Windows peer route journal: %w", err)
	}
	j.record = &record
	return record, nil
}

func (j *windowsPeerRouteJournal) removeLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := j.store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Windows peer route journal: %w", err)
	}
	j.record = nil
	return nil
}

func (j *windowsPeerRouteJournal) quarantineLocked(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := j.store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := quarantineWindowsRouteState(j.path, reason); err != nil {
		return err
	}
	j.record = nil
	return nil
}
