package smoke

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func openPrivacyArtifactRoot(root string) (*os.File, error) {
	clean := filepath.Clean(root)
	if root == "" || !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
		return nil, errPrivacyArtifactStore
	}
	// macOS exposes /var as a system-owned compatibility symlink. Normalize only that trusted
	// platform alias; every caller-controlled ancestor is still traversed with O_NOFOLLOW.
	if runtime.GOOS == "darwin" && (clean == "/var" || strings.HasPrefix(clean, "/var/")) {
		clean = "/private" + clean
	}
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(rootFD), "privacy-artifact-root")
	if current == nil {
		_ = unix.Close(rootFD)
		return nil, errPrivacyArtifactStore
	}
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			_ = current.Close()
			return nil, errPrivacyArtifactStore
		}
		last := index == len(parts)-1
		fd, openErr := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(openErr, unix.ENOENT) && last {
			if unix.Mkdirat(int(current.Fd()), part, 0o700) != nil {
				_ = current.Close()
				return nil, errPrivacyArtifactStore
			}
			fd, openErr = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, errPrivacyArtifactStore
		}
		next := os.NewFile(uintptr(fd), "privacy-artifact-directory")
		if next == nil {
			_ = unix.Close(fd)
			_ = current.Close()
			return nil, errPrivacyArtifactStore
		}
		_ = current.Close()
		current = next
	}
	if !validOpenPrivacyArtifactDirectory(current) {
		_ = current.Close()
		return nil, errPrivacyArtifactStore
	}
	return current, nil
}

func validOpenPrivacyArtifactDirectory(directory *os.File) bool {
	if directory == nil {
		return false
	}
	var stat unix.Stat_t
	if unix.Fstat(int(directory.Fd()), &stat) != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Uid == uint32(os.Geteuid()) && stat.Mode&0o7777 == 0o700
}

func lockPrivacyArtifactDirectory(ctx context.Context, directory *os.File, exclusive bool) error {
	if !privacyArtifactLockContext(ctx) || directory == nil {
		return errPrivacyArtifactStore
	}
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	for {
		err := unix.Flock(int(directory.Fd()), operation|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return errPrivacyArtifactStore
		}
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return errPrivacyArtifactStore
		case <-timer.C:
		}
	}
}

func unlockPrivacyArtifactDirectory(directory *os.File) {
	if directory != nil {
		_ = unix.Flock(int(directory.Fd()), unix.LOCK_UN)
	}
}

func privacyArtifactFinalExists(directory *os.File, files []privacyArtifactPreparedFile) bool {
	for _, file := range files {
		var stat unix.Stat_t
		err := unix.Fstatat(int(directory.Fd()), file.ref, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil || !errors.Is(err, unix.ENOENT) {
			return true
		}
	}
	return false
}

func publishPrivacyArtifactAt(directory *os.File, final string, payload []byte) error {
	if directory == nil || !safePrivacyArtifactRef(final) || len(payload) == 0 || int64(len(payload)) > maximumPrivacyArtifactBytes {
		return errPrivacyArtifactStore
	}
	token, err := randomManifestToken()
	if err != nil {
		return errPrivacyArtifactStore
	}
	temporary := ".tmp-privacy-" + token
	fd, err := unix.Openat(int(directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return errPrivacyArtifactStore
	}
	file := os.NewFile(uintptr(fd), temporary)
	if file == nil {
		_ = unix.Close(fd)
		_ = unlinkPrivacyArtifactAt(directory, temporary)
		return errPrivacyArtifactStore
	}
	cleanup := func() {
		_ = file.Close()
		_ = unlinkPrivacyArtifactAt(directory, temporary)
	}
	if !validOpenPrivacyArtifactFile(file, int64(len(payload))) {
		cleanup()
		return errPrivacyArtifactStore
	}
	written := 0
	for written < len(payload) {
		count, writeErr := file.Write(payload[written:])
		if writeErr != nil || count <= 0 {
			cleanup()
			return errPrivacyArtifactStore
		}
		written += count
	}
	if file.Sync() != nil || !validCompletedPrivacyArtifactFile(file, int64(len(payload))) {
		cleanup()
		return errPrivacyArtifactStore
	}
	if file.Close() != nil {
		_ = unlinkPrivacyArtifactAt(directory, temporary)
		return errPrivacyArtifactStore
	}
	if unix.Linkat(int(directory.Fd()), temporary, int(directory.Fd()), final, 0) != nil {
		_ = unlinkPrivacyArtifactAt(directory, temporary)
		return errPrivacyArtifactStore
	}
	if unlinkPrivacyArtifactAt(directory, temporary) != nil || directory.Sync() != nil {
		_ = unlinkPrivacyArtifactAt(directory, final)
		_ = directory.Sync()
		return errPrivacyArtifactStore
	}
	return nil
}

func readPrivacyArtifactAt(directory *os.File, name string) ([]byte, error) {
	if directory == nil || !safePrivacyArtifactRef(name) {
		return nil, errPrivacyArtifactStore
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errPrivacyArtifactStore
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errPrivacyArtifactStore
	}
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !validPrivacyArtifactStat(before) {
		return nil, errPrivacyArtifactStore
	}
	payload, err := io.ReadAll(io.LimitReader(file, maximumPrivacyArtifactBytes+1))
	if err != nil || int64(len(payload)) == 0 || int64(len(payload)) > maximumPrivacyArtifactBytes || int64(len(payload)) != before.Size {
		return nil, errPrivacyArtifactStore
	}
	var after unix.Stat_t
	if unix.Fstat(fd, &after) != nil || !samePrivacyArtifactStat(before, after) {
		return nil, errPrivacyArtifactStore
	}
	return payload, nil
}

func validOpenPrivacyArtifactFile(file *os.File, expectedSize int64) bool {
	if file == nil {
		return false
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 {
		return false
	}
	return stat.Size == 0 || stat.Size == expectedSize
}

func validCompletedPrivacyArtifactFile(file *os.File, expectedSize int64) bool {
	if file == nil {
		return false
	}
	var stat unix.Stat_t
	return unix.Fstat(int(file.Fd()), &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG &&
		stat.Uid == uint32(os.Geteuid()) && stat.Mode&0o7777 == 0o600 && stat.Nlink == 1 && stat.Size == expectedSize
}

func validPrivacyArtifactStat(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Mode&0o7777 == 0o600 &&
		stat.Nlink == 1 && stat.Size > 0 && stat.Size <= maximumPrivacyArtifactBytes
}

func samePrivacyArtifactStat(before, after unix.Stat_t) bool {
	return before.Dev == after.Dev && before.Ino == after.Ino && before.Mode == after.Mode && before.Uid == after.Uid &&
		before.Nlink == after.Nlink && before.Size == after.Size && validPrivacyArtifactStat(after)
}

func unlinkPrivacyArtifactAt(directory *os.File, name string) error {
	if directory == nil || name == "" || name != filepath.Base(name) {
		return errPrivacyArtifactStore
	}
	return unix.Unlinkat(int(directory.Fd()), name, 0)
}
