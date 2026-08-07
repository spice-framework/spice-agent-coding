//go:build windows

package userstorage

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type stableLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

type secureDirectory struct {
	mu     sync.RWMutex
	path   string
	handle windows.Handle
}

func bindSecureDirectory(path string) (*secureDirectory, error) {
	if err := prepareSecureDirectory(path); err != nil {
		return nil, err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return nil, err
	}
	if err = validateWindowsHandle(handle, path); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if err = validateWindowsAncestry(path, handle); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &secureDirectory{path: path, handle: handle}, nil
}

func prepareSecureDirectory(path string) error {
	if err := validateLocalPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		security, securityErr := currentUserSecurity()
		if securityErr != nil {
			return securityErr
		}
		if err = createSecureDirectory(path, security); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return ErrUnavailable
	}
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	return validateWindowsPath(path)
}

func acquireStableLock(path string) (*stableLock, error) {
	return acquireWindowsLock(path, false)
}

func acquireWindowsLock(path string, wait bool) (*stableLock, error) {
	file, err := openWindowsFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.OPEN_ALWAYS)
	if err != nil {
		return nil, err
	}
	if err = validateWindowsHandle(windows.Handle(file.Fd()), path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return lockWindowsFile(file, wait)
}

func lockWindowsFile(file *os.File, wait bool) (*stableLock, error) {
	lock := &stableLock{file: file}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if wait {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lock.overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, errLockBusy
		}
		return nil, err
	}
	return lock, nil
}

func (directory *secureDirectory) acquireStableLock(name string) (*stableLock, error) {
	return directory.acquireLock(name, false)
}

func (directory *secureDirectory) acquireInitializationLock(name string) (*stableLock, error) {
	return directory.acquireLock(name, true)
}

func (directory *secureDirectory) acquireLock(name string, wait bool) (*stableLock, error) {
	if !validRelativeName(name) {
		return nil, ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.handle == windows.InvalidHandle {
		return nil, ErrUnavailable
	}
	file, err := openRelativeWindowsFile(
		directory.handle, name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF,
	)
	if err != nil {
		return nil, err
	}
	return lockWindowsFile(file, wait)
}

func (directory *secureDirectory) close() error {
	if directory == nil {
		return nil
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if directory.handle == windows.InvalidHandle {
		return nil
	}
	closeErr := windows.CloseHandle(directory.handle)
	directory.handle = windows.InvalidHandle
	return closeErr
}

func (lock *stableLock) close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	handle := windows.Handle(lock.file.Fd())
	unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, &lock.overlapped)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}

func readSecureFile(path string, maximum int) ([]byte, error) {
	file, err := openWindowsFile(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if err = validateWindowsHandle(windows.Handle(file.Fd()), path); err != nil {
		return nil, err
	}
	value, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(value) > maximum {
		return nil, ErrUnavailable
	}
	return value, nil
}

func (directory *secureDirectory) readFile(name string, maximum int) ([]byte, error) {
	if !validRelativeName(name) {
		return nil, ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.handle == windows.InvalidHandle {
		return nil, ErrUnavailable
	}
	file, err := openRelativeWindowsFile(directory.handle, name, windows.FILE_GENERIC_READ, windows.FILE_OPEN)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	value, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(value) > maximum {
		return nil, ErrUnavailable
	}
	return value, nil
}

func writeSecureFileAtomic(path string, value []byte) error {
	if err := validateLocalPath(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err = validateWindowsPath(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var random [12]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(random[:])+".tmp")
	file, err := openWindowsFile(temporary, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.CREATE_NEW)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err = file.Write(value); err != nil {
		return err
	}
	if err = windows.FlushFileBuffers(windows.Handle(file.Fd())); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
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
	if err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func (directory *secureDirectory) writeFileAtomic(name string, value []byte) error {
	if !validRelativeName(name) {
		return ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.handle == windows.InvalidHandle {
		return ErrUnavailable
	}
	if err := validateRelativeWindowsDestination(directory.handle, name); err != nil {
		return err
	}
	var random [12]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return err
	}
	temporary := "." + name + "." + hex.EncodeToString(random[:]) + ".tmp"
	file, err := openRelativeWindowsFile(
		directory.handle, temporary,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, windows.FILE_CREATE,
	)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = deleteWindowsFile(windows.Handle(file.Fd()))
		}
		_ = file.Close()
	}()
	if _, err = file.Write(value); err != nil {
		return err
	}
	if err = windows.FlushFileBuffers(windows.Handle(file.Fd())); err != nil {
		return err
	}
	if err = renameWindowsFile(windows.Handle(file.Fd()), directory.handle, name); err != nil {
		return err
	}
	if err = windows.FlushFileBuffers(windows.Handle(file.Fd())); err != nil {
		return err
	}
	renamed = true
	return nil
}

func (directory *secureDirectory) removeFile(name string) error {
	if !validRelativeName(name) {
		return ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.handle == windows.InvalidHandle {
		return ErrUnavailable
	}
	file, err := openRelativeWindowsFile(
		directory.handle, name, windows.FILE_GENERIC_READ|windows.DELETE, windows.FILE_OPEN,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return deleteWindowsFile(windows.Handle(file.Fd()))
}

func openRelativeWindowsFile(
	directory windows.Handle,
	name string,
	access uint32,
	disposition uint32,
) (*os.File, error) {
	if !validRelativeName(name) {
		return nil, ErrUnavailable
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	security, err := currentUserSecurity()
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: directory,
		ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: security,
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle, access, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, disposition,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
		0, 0,
	)
	if errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) {
		return nil, os.ErrNotExist
	}
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
		return nil, os.ErrExist
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	if err = validateRelativeWindowsFile(handle); err != nil {
		_ = file.Close()
		return nil, err
	}
	runtime.KeepAlive(security)
	return file, nil
}

func validateRelativeWindowsFile(handle windows.Handle) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
		information.NumberOfLinks != 1 {
		return ErrUnavailable
	}
	return validateWindowsSecurity(handle)
}

func validateRelativeWindowsDestination(directory windows.Handle, name string) error {
	file, err := openRelativeWindowsFile(directory, name, windows.FILE_GENERIC_READ, windows.FILE_OPEN)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func renameWindowsFile(file, directory windows.Handle, name string) error {
	utf16Name, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameBytes := (len(utf16Name) - 1) * 2
	var layout fileRenameInformation
	headerSize := int(unsafe.Offsetof(layout.FileName))
	buffer := make([]byte, headerSize+nameBytes)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0])) // #nosec G103 -- buffer matches FILE_RENAME_INFORMATION.
	information.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS
	information.RootDirectory = directory
	information.FileNameLength = uint32(nameBytes)                                                // #nosec G115 -- relative names are bounded by the Windows component limit.
	target := unsafe.Slice((*uint16)(unsafe.Pointer(&information.FileName[0])), len(utf16Name)-1) // #nosec G103 -- bounded by allocated filename bytes.
	copy(target, utf16Name[:len(utf16Name)-1])
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		file, &status, &buffer[0], uint32(len(buffer)), // #nosec G115 -- one bounded Windows path component plus a fixed header.
		windows.FileRenameInformation,
	)
}

func deleteWindowsFile(file windows.Handle) error {
	flags := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS)
	var buffer [4]byte
	binary.LittleEndian.PutUint32(buffer[:], flags)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(
		file, &status, &buffer[0], 4, windows.FileDispositionInformationEx,
	)
}

func openWindowsFile(path string, access, disposition uint32) (*os.File, error) {
	if err := validateLocalPath(path); err != nil {
		return nil, err
	}
	if err := validateWindowsComponents(filepath.Dir(path)); err != nil {
		return nil, err
	}
	security, err := currentUserSecurity()
	if err != nil {
		return nil, err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: security}
	handle, err := windows.CreateFile(
		pointer, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, attributes, disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if err = validateFinalHandlePath(handle, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateLocalPath(path string) error {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
		return ErrUnavailable
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		return ErrUnavailable
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return err
	}
	switch windows.GetDriveType(root) {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_RAMDISK:
		return nil
	default:
		return ErrUnavailable
	}
}

func validateWindowsHandle(handle windows.Handle, path string) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || information.NumberOfLinks != 1 {
		return ErrUnavailable
	}
	if err := validateFinalHandlePath(handle, path); err != nil {
		return err
	}
	return validateWindowsSecurity(handle)
}

func validateWindowsAncestry(path string, expected windows.Handle) error {
	trusted, err := trustedWindowsSIDs()
	if err != nil {
		return err
	}
	var final windows.ByHandleFileInformation
	paths, err := windowsAncestryPaths(path)
	if err != nil {
		return err
	}
	for _, current := range paths {
		final, err = validateWindowsAncestor(current, trusted)
		if err != nil {
			return err
		}
	}
	var expectedInformation windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(expected, &expectedInformation); err != nil ||
		final.VolumeSerialNumber != expectedInformation.VolumeSerialNumber ||
		final.FileIndexHigh != expectedInformation.FileIndexHigh ||
		final.FileIndexLow != expectedInformation.FileIndexLow {
		return ErrUnavailable
	}
	return nil
}

func windowsAncestryPaths(path string) ([]string, error) {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	if volume == "" || strings.HasPrefix(volume, `\\`) {
		return nil, ErrUnavailable
	}
	current := volume + string(filepath.Separator)
	result := []string{current}
	remainder := strings.TrimPrefix(cleaned[len(volume):], string(filepath.Separator))
	for component := range strings.SplitSeq(remainder, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		result = append(result, current)
	}
	return result, nil
}

func validateWindowsAncestor(
	path string,
	trusted []*windows.SID,
) (windows.ByHandleFileInformation, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	handle, err := windows.CreateFile(
		pointer, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return windows.ByHandleFileInformation{}, ErrUnavailable
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var information windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &information); err != nil ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return windows.ByHandleFileInformation{}, ErrUnavailable
	}
	if err = validateFinalHandlePath(handle, path); err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	if err = validateWindowsAncestrySecurity(handle, trusted); err != nil {
		return windows.ByHandleFileInformation{}, err
	}
	return information, nil
}

func validateWindowsAncestrySecurity(handle windows.Handle, trusted []*windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil {
		return ErrUnavailable
	}
	defer runtime.KeepAlive(descriptor)
	owner, _, err := descriptor.Owner()
	if err != nil || !trustedWindowsSID(owner, trusted) {
		return ErrUnavailable
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return ErrUnavailable
	}
	const fileDeleteChild windows.ACCESS_MASK = 0x00000040
	dangerous := windows.ACCESS_MASK(
		windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_ALL,
	) | fileDeleteChild
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return ErrUnavailable
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) // #nosec G103 -- GetAce returns its SID at SidStart.
			if ace.Mask&dangerous != 0 && !trustedWindowsSID(aceSID, trusted) {
				return ErrUnavailable
			}
		default:
			// Object/callback ACE semantics are deliberately rejected instead of
			// approximated for the authority ancestry boundary.
			return ErrUnavailable
		}
	}
	return nil
}

func trustedWindowsSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	trustedInstaller, err := windows.StringToSid(
		"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464",
	)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{user.User.Sid, system, administrators, trustedInstaller}, nil
}

func trustedWindowsSID(candidate *windows.SID, trusted []*windows.SID) bool {
	if candidate == nil {
		return false
	}
	return slices.ContainsFunc(trusted, candidate.Equals)
}

func validateWindowsPath(path string) error {
	if err := validateLocalPath(path); err != nil {
		return err
	}
	if err := validateWindowsComponents(path); err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		pointer, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, flags, 0,
	)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return validateWindowsHandle(handle, path)
}

func validateWindowsSecurity(handle windows.Handle) error {
	descriptor, err := windows.GetSecurityInfo(
		handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil {
		return ErrUnavailable
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return ErrUnavailable
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !owner.Equals(user.User.Sid) {
		return ErrUnavailable
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnavailable
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return ErrUnavailable
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err = windows.GetAce(dacl, 0, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return ErrUnavailable
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) // #nosec G103 -- GetAce returns its SID starting at SidStart.
	if !aceSID.Equals(user.User.Sid) {
		return ErrUnavailable
	}
	runtime.KeepAlive(descriptor)
	return nil
}

func validateFinalHandlePath(handle windows.Handle, expected string) error {
	buffer := make([]uint16, 512)
	for {
		bufferLength := uint32(len(buffer)) // #nosec G115 -- buffer is initialized at 512 and capped at 32768 below.
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], bufferLength, 0)
		if err != nil {
			return ErrUnavailable
		}
		if length < bufferLength {
			actual := windows.UTF16ToString(buffer[:length])
			if !strings.EqualFold(normalizeWindowsPath(actual), normalizeWindowsPath(expected)) {
				return ErrUnavailable
			}
			return nil
		}
		if length > 32767 {
			return ErrUnavailable
		}
		buffer = make([]uint16, length+1)
	}
}

func normalizeWindowsPath(path string) string {
	if remainder, found := strings.CutPrefix(path, `\\?\UNC\`); found {
		path = `\\` + remainder
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	return filepath.Clean(path)
}

func currentUserSecurity() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;GA;;;" + user.User.Sid.String() + ")")
}

func createSecureDirectory(path string, security *windows.SECURITY_DESCRIPTOR) error {
	if _, err := os.Lstat(path); err == nil {
		return validateWindowsComponents(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		if err = createSecureDirectory(parent, security); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err = validateWindowsComponents(parent); err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: security,
	}
	if err = windows.CreateDirectory(pointer, attributes); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return validateWindowsPath(path)
		}
		return err
	}
	return nil
}

func validateWindowsComponents(path string) error {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	if volume == "" || strings.HasPrefix(volume, `\\`) {
		return ErrUnavailable
	}
	current := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(cleaned[len(volume):], string(filepath.Separator))
	if remainder == "" {
		return nil
	}
	for component := range strings.SplitSeq(remainder, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return ErrUnavailable
		}
	}
	return nil
}
