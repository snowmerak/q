package diagnostics

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/worklock"
)

var logFileName = regexp.MustCompile(`^studio-\d{8}T\d{6}\.\d{9}Z-[a-f0-9]{32}-\d{6,}\.jsonl$`)

type fileStore struct {
	dir      string
	options  Options
	instance string
	sequence uint64
	current  string
	day      string
	now      func() time.Time
}

func (s *fileStore) lock() (*worklock.Lock, error) {
	// Do not follow a linked logs/studio directory into another data store.
	for _, dir := range []string{filepath.Dir(s.dir), s.dir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("diagnostics: linked log directory")
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	if info, err := os.Lstat(filepath.Join(s.dir, ".retention.lock")); err == nil && (!info.Mode().IsRegular()) {
		return nil, errors.New("diagnostics: invalid retention lock")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for {
		lock, err := worklock.AcquireFile(s.dir, ".retention.lock", "Studio log retention")
		if !errors.Is(err, worklock.ErrLocked) || !time.Now().Before(deadline) {
			return lock, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type logFile struct {
	name     string
	size     int64
	modified time.Time
}

// prune reserves the upcoming record's bytes while all cooperating processes
// hold the same OS lock. Only this store's managed regular files are candidates.
func (s *fileStore) prune(reserve int64, keep string) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	cutoff := s.now().Add(-s.options.MaxAge)
	var files []logFile
	var total int64
	for _, entry := range entries {
		if !logFileName.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if entry.Name() != keep && (info.ModTime().Before(cutoff) || info.Size() > s.options.MaxFileBytes) {
			if err := s.remove(entry.Name()); err != nil {
				return err
			}
			continue
		}
		if info.Size() > (1<<63-1)-total {
			return errors.New("diagnostics: log sizes overflow")
		}
		total += info.Size()
		files = append(files, logFile{entry.Name(), info.Size(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].name < files[j].name
		}
		return files[i].modified.Before(files[j].modified)
	})
	for _, file := range files {
		if total <= s.options.MaxTotalBytes-reserve {
			return nil
		}
		if file.name == keep {
			continue
		}
		if err := s.remove(file.name); err != nil {
			return err
		}
		total -= file.size
	}
	if total > s.options.MaxTotalBytes-reserve {
		return errors.New("diagnostics: log budget unavailable")
	}
	return nil
}

func (s *fileStore) remove(name string) error {
	if !logFileName.MatchString(name) || filepath.Base(name) != name {
		return errors.New("diagnostics: invalid deletion target")
	}
	return os.Remove(filepath.Join(s.dir, name))
}

func (s *fileStore) maintain() (result error) {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	return s.prune(0, "")
}

func (s *fileStore) write(batch [][]byte) (result error) {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	if err := s.prune(0, ""); err != nil {
		return err
	}
	for _, body := range batch {
		if len(body) == 0 {
			continue
		}
		if int64(len(body)) > s.options.MaxFileBytes {
			return errors.New("diagnostics: record exceeds file size")
		}
		now := s.now().UTC()
		var size int64
		if s.current != "" {
			info, err := os.Lstat(filepath.Join(s.dir, s.current))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err != nil {
				s.current = ""
			} else if !info.Mode().IsRegular() {
				return errors.New("diagnostics: invalid log file")
			} else {
				size = info.Size()
			}
		}
		fresh := s.current == "" || s.day != now.Format("20060102") || size > s.options.MaxFileBytes-int64(len(body))
		if fresh {
			s.sequence++
			s.current = fmt.Sprintf("studio-%s-%s-%06d.jsonl", now.Format("20060102T150405.000000000Z"), s.instance, s.sequence)
			s.day = now.Format("20060102")
			size = 0
		}
		if err := s.prune(int64(len(body)), s.current); err != nil {
			return err
		}
		flags := os.O_WRONLY | os.O_APPEND
		if fresh {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := os.OpenFile(filepath.Join(s.dir, s.current), flags, 0o600)
		if err != nil {
			s.current = ""
			return err
		}
		n, writeErr := file.Write(body)
		if writeErr == nil && n != len(body) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			writeErr = errors.Join(writeErr, file.Truncate(size))
			s.current = ""
		}
		if err := errors.Join(writeErr, file.Close()); err != nil {
			s.current = ""
			return err
		}
	}
	return nil
}

func newFileStore(directory, instance string, options Options) *fileStore {
	return &fileStore{dir: filepath.Join(directory, "logs", "studio"), instance: strings.ReplaceAll(instance, "-", ""), options: options, now: time.Now}
}
