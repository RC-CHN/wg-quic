//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsRouteLedgerFile = "routes-v1.json"
	windowsRouteBackupFile = "routes-v1.json.bak"
	windowsRouteLockFile   = "routes-v1.lock"
	windowsRouteMaxSize    = 16 << 20
)

type windowsDiskRouteStore struct {
	stateDirectory  string
	ownersDirectory string
	ledgerPath      string
	backupPath      string
	lockPath        string
}

type windowsOwnerLease struct {
	file *os.File
	path string
	once sync.Once
	err  error
}

var windowsRouteProcessLock = func() chan struct{} {
	lock := make(chan struct{}, 1)
	lock <- struct{}{}
	return lock
}()

func newWindowsRouteManager(tunnel string) (*windowsRouteManager, error) {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	store := &windowsDiskRouteStore{
		stateDirectory:  filepath.Join(root, "wg-quic", "state"),
		ownersDirectory: filepath.Join(root, "wg-quic", "state", "owners"),
		ledgerPath:      filepath.Join(root, "wg-quic", "state", windowsRouteLedgerFile),
		backupPath:      filepath.Join(root, "wg-quic", "state", windowsRouteBackupFile),
		lockPath:        filepath.Join(root, "wg-quic", "state", windowsRouteLockFile),
	}
	if err := store.prepare(); err != nil {
		return nil, err
	}
	owner, lease, err := store.createOwnerLease(tunnel)
	if err != nil {
		return nil, err
	}
	tunnelLUID, err := windowsInterfaceLUID(tunnel)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	notifier, err := newWindowsRouteNotifier()
	if err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("subscribe to Windows route changes: %w", err)
	}
	manager := &windowsRouteManager{
		system:     windowsNativeRouteSystem{},
		store:      store,
		owner:      owner,
		tunnelLUID: tunnelLUID,
		closeOwner: lease.Close,
	}
	manager.startRouteWatcher(notifier)
	return manager, nil
}

func (s *windowsDiskRouteStore) prepare() error {
	for _, directory := range []string{s.stateDirectory, s.ownersDirectory} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create Windows route state directory: %w", err)
		}
		attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(directory))
		if err != nil {
			return fmt.Errorf("inspect Windows route state directory: %w", err)
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("Windows route state directory %q is a reparse point", directory)
		}
		if err := protectWindowsRoutePath(directory); err != nil {
			return err
		}
	}
	return nil
}

func (s *windowsDiskRouteStore) createOwnerLease(
	tunnel string,
) (windowsRouteOwner, *windowsOwnerLease, error) {
	guid, err := windows.GenerateGUID()
	if err != nil {
		return windowsRouteOwner{}, nil, fmt.Errorf("generate route owner UUID: %w", err)
	}
	instanceID := strings.ToLower(strings.Trim(guid.String(), "{}"))
	filename := instanceID + ".lease"
	path := filepath.Join(s.ownersDirectory, filename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return windowsRouteOwner{}, nil, fmt.Errorf("create route owner lease: %w", err)
	}
	closeWithError := func(cause error) (windowsRouteOwner, *windowsOwnerLease, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return windowsRouteOwner{}, nil, cause
	}
	if err := protectWindowsRoutePath(path); err != nil {
		return closeWithError(err)
	}
	if _, err := fmt.Fprintf(file, "wg-quic route owner\ninstance=%s\ntunnel=%s\n", instanceID, tunnel); err != nil {
		return closeWithError(fmt.Errorf("write route owner lease: %w", err))
	}
	if err := file.Sync(); err != nil {
		return closeWithError(fmt.Errorf("flush route owner lease: %w", err))
	}
	if err := lockWindowsFile(file, false); err != nil {
		return closeWithError(fmt.Errorf("lock route owner lease: %w", err))
	}
	return windowsRouteOwner{
			Tunnel:     tunnel,
			InstanceID: instanceID,
			LeaseFile:  filename,
			References: 1,
		},
		&windowsOwnerLease{file: file, path: path},
		nil
}

func (l *windowsOwnerLease) Close() error {
	l.once.Do(func() {
		var errs []error
		if l.file != nil {
			if err := unlockWindowsFile(l.file); err != nil {
				errs = append(errs, fmt.Errorf("unlock route owner lease: %w", err))
			}
			if err := l.file.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close route owner lease: %w", err))
			}
		}
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove route owner lease: %w", err))
		}
		l.err = errors.Join(errs...)
	})
	return l.err
}

func (s *windowsDiskRouteStore) Lock(ctx context.Context) (func() error, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-windowsRouteProcessLock:
	}
	file, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		windowsRouteProcessLock <- struct{}{}
		return nil, err
	}
	if err := protectWindowsRoutePath(s.lockPath); err != nil {
		_ = file.Close()
		windowsRouteProcessLock <- struct{}{}
		return nil, err
	}
	for {
		err = lockWindowsFile(file, true)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = file.Close()
			windowsRouteProcessLock <- struct{}{}
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			windowsRouteProcessLock <- struct{}{}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	var once sync.Once
	var unlockErr error
	return func() error {
		once.Do(func() {
			unlockErr = errors.Join(unlockWindowsFile(file), file.Close())
			windowsRouteProcessLock <- struct{}{}
		})
		return unlockErr
	}, nil
}

func (s *windowsDiskRouteStore) OwnerAlive(owner windowsRouteOwner) (bool, error) {
	if !validWindowsRouteOwnerID(owner.InstanceID) ||
		owner.LeaseFile != owner.InstanceID+".lease" ||
		filepath.Base(owner.LeaseFile) != owner.LeaseFile {
		return false, fmt.Errorf("invalid owner lease filename %q", owner.LeaseFile)
	}
	path := filepath.Join(s.ownersDirectory, owner.LeaseFile)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	err = lockWindowsFile(file, true)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err := unlockWindowsFile(file); err != nil {
		return false, err
	}
	_ = file.Close()
	_ = os.Remove(path)
	return false, nil
}

func (s *windowsDiskRouteStore) Load() (windowsRouteLedger, error) {
	ledger, err := loadWindowsRouteLedgerFile(s.ledgerPath)
	if err == nil {
		return ledger, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		if quarantineErr := quarantineWindowsRouteLedger(s.ledgerPath); quarantineErr != nil {
			return windowsRouteLedger{}, errors.Join(err, quarantineErr)
		}
	}
	backup, backupErr := loadWindowsRouteLedgerFile(s.backupPath)
	if backupErr == nil {
		// A backup is necessarily older than the missing/corrupt primary.
		// Preserve its route keys for borrowing, but never use stale state as
		// proof that a live kernel route is ours to delete.
		for index := range backup.Routes {
			backup.Routes[index].Ownership = windowsRouteAmbiguous
			backup.Routes[index].State = windowsRouteActive
		}
		return backup, nil
	}
	if !errors.Is(backupErr, os.ErrNotExist) {
		if quarantineErr := quarantineWindowsRouteLedger(s.backupPath); quarantineErr != nil {
			return windowsRouteLedger{}, errors.Join(err, backupErr, quarantineErr)
		}
	}
	return windowsRouteLedger{
		SchemaVersion: windowsRouteLedgerSchemaVersion,
		Routes:        []windowsRouteRecord{},
	}, nil
}

func (s *windowsDiskRouteStore) Save(ledger *windowsRouteLedger) error {
	encoded, err := encodeWindowsRouteLedger(*ledger)
	if err != nil {
		return err
	}
	if previous, readErr := os.ReadFile(s.ledgerPath); readErr == nil {
		if _, validateErr := decodeWindowsRouteLedger(previous); validateErr == nil {
			if err := s.atomicWrite(s.backupPath, previous); err != nil {
				return fmt.Errorf("write Windows route ledger backup: %w", err)
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := s.atomicWrite(s.ledgerPath, encoded); err != nil {
		return fmt.Errorf("write Windows route ledger: %w", err)
	}
	*ledger, err = decodeWindowsRouteLedger(encoded)
	return err
}

func (s *windowsDiskRouteStore) atomicWrite(path string, data []byte) error {
	guid, err := windows.GenerateGUID()
	if err != nil {
		return err
	}
	temporary := path + "." + strings.Trim(guid.String(), "{}") + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	keepTemporary := true
	defer func() {
		_ = file.Close()
		if keepTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := protectWindowsRoutePath(temporary); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(
		from,
		to,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	); err != nil {
		return err
	}
	keepTemporary = false
	return protectWindowsRoutePath(path)
}

func loadWindowsRouteLedgerFile(path string) (windowsRouteLedger, error) {
	file, err := os.Open(path)
	if err != nil {
		return windowsRouteLedger{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, windowsRouteMaxSize+1))
	if err != nil {
		return windowsRouteLedger{}, err
	}
	if len(data) > windowsRouteMaxSize {
		return windowsRouteLedger{}, errors.New("Windows route ledger is too large")
	}
	return decodeWindowsRouteLedger(data)
}

func quarantineWindowsRouteLedger(path string) error {
	target := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	from, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("quarantine corrupt Windows route ledger: %w", err)
	}
	return nil
}

func lockWindowsFile(file *os.File, nonblocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		flags,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

func unlockWindowsFile(file *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&windows.Overlapped{},
	)
}

func protectWindowsRoutePath(path string) error {
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;;FA;;;SY)(A;;FA;;;BA)",
	)
	if err != nil {
		return fmt.Errorf("create Windows route state ACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read Windows route state ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("protect Windows route state path %q: %w", path, err)
	}
	return nil
}
