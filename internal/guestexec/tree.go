package guestexec

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Excluded reports whether rel (slash-separated, relative to the tree's root) matches one of the
// patterns. A pattern ending with "/" matches a directory of that name at any depth
// ("node_modules/"); any other pattern is matched against the base name ("*.pyc"). This is the
// subset of .gitignore that internal/job's exclude list uses.
func Excluded(rel string, isDir bool, patterns []string) bool {
	base := path.Base(rel)

	for _, p := range patterns {
		if name, ok := strings.CutSuffix(p, "/"); ok {
			if isDir && base == name {
				return true
			}

			continue
		}

		if ok, _ := path.Match(p, base); ok {
			return true
		}
	}

	return false
}

// TreeOptions adjust WriteTree.
type TreeOptions struct {
	Exclude []string
	// Owner, when set, replaces every file's owner in the tar: the guest sees the files as its
	// agent user whatever the host's UID is.
	Owner *[2]int
}

// WriteTree writes dir as a tar stream: directories, regular files and symbolic links, with
// their modes. Other file types are skipped. Paths are relative to dir.
func WriteTree(w io.Writer, dir string, o TreeOptions) error {
	tw := tar.NewWriter(w)

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}

		rel = filepath.ToSlash(rel)
		if Excluded(rel, d.IsDir(), o.Exclude) {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}

		hdr.Name = rel
		if info.IsDir() {
			hdr.Name += "/"
		}

		hdr.Uname, hdr.Gname = "", ""
		if o.Owner != nil {
			hdr.Uid, hdr.Gid = o.Owner[0], o.Owner[1]
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(p)
		if err != nil {
			return err
		}

		_, err = io.Copy(tw, f)
		_ = f.Close()

		return err
	})
	if err != nil {
		return err
	}

	return tw.Close()
}

// MirrorOptions bound what a guest may send.
type MirrorOptions struct {
	Exclude    []string // left alone on the host: neither written nor deleted
	MaxBytes   int64    // total file content; 0: no limit
	MaxEntries int      // 0: no limit
}

// ErrOutside is returned for an entry that would land outside the target directory.
var ErrOutside = errors.New("path outside the workspace")

// Mirror makes dir match the tar stream r: entries are created or replaced, and what is in dir
// but not in the stream is removed (except excluded paths). The stream is hostile. Every entry is
// resolved under dir with openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS), so ".." components,
// absolute paths and symbolic links planted by an earlier entry cannot lead outside dir.
func Mirror(dir string, r io.Reader, o MirrorOptions) error {
	root, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = unix.Close(root) }()

	m := &mirror{root: root, dir: dir, o: o, seen: map[string]bool{}}

	tr := tar.NewReader(r)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("workspace tar: %w", err)
		}

		if err := m.entry(hdr, tr); err != nil {
			return fmt.Errorf("%q: %w", hdr.Name, err)
		}
	}

	return m.prune()
}

type mirror struct {
	root    int
	dir     string
	o       MirrorOptions
	seen    map[string]bool
	bytes   int64
	entries int
}

func (m *mirror) entry(hdr *tar.Header, r io.Reader) error {
	rel, err := cleanRel(hdr.Name)
	if err != nil {
		return err
	}

	if rel == "." {
		return nil
	}

	m.entries++
	if m.o.MaxEntries > 0 && m.entries > m.o.MaxEntries {
		return fmt.Errorf("more than %d entries", m.o.MaxEntries)
	}

	isDir := hdr.Typeflag == tar.TypeDir
	if Excluded(rel, isDir, m.o.Exclude) {
		return nil
	}

	parent, base := path.Split(rel)

	pfd, err := m.openDir(strings.TrimSuffix(parent, "/"))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pfd) }()

	mode := uint32(hdr.Mode) & 0o777

	switch hdr.Typeflag {
	case tar.TypeDir:
		var st unix.Stat_t

		err := unix.Fstatat(pfd, base, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := m.remove(rel); err != nil {
				return err
			}
		}

		if err := unix.Mkdirat(pfd, base, mode|0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}

		if err := unix.Fchmodat(pfd, base, mode|0o700, 0); err != nil {
			return err
		}

	case tar.TypeReg:
		m.bytes += hdr.Size
		if m.o.MaxBytes > 0 && m.bytes > m.o.MaxBytes {
			return fmt.Errorf("more than %d bytes", m.o.MaxBytes)
		}

		if err := m.remove(rel); err != nil {
			return err
		}

		fd, err := unix.Openat(pfd, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		if err != nil {
			return err
		}

		f := os.NewFile(uintptr(fd), rel)
		_, err = io.CopyN(f, r, hdr.Size)

		if cerr := f.Close(); err == nil {
			err = cerr
		}

		if err != nil {
			return err
		}

	case tar.TypeSymlink:
		if err := m.remove(rel); err != nil {
			return err
		}

		if err := unix.Symlinkat(hdr.Linkname, pfd, base); err != nil {
			return err
		}

	default:
		return nil // devices, FIFOs, hard links: not part of a workspace
	}

	m.seen[rel] = true

	return nil
}

// openDir opens rel, a directory under the root, refusing any symbolic link on the way.
func (m *mirror) openDir(rel string) (int, error) {
	if rel == "" {
		rel = "."
	}

	fd, err := unix.Openat2(m.root, rel, &unix.OpenHow{
		Flags:   unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
		return -1, ErrOutside
	}

	return fd, err
}

// remove deletes rel if it exists, whatever its type. Its parent was just opened without
// following symbolic links, so the path stays under the root.
func (m *mirror) remove(rel string) error {
	err := os.RemoveAll(filepath.Join(m.dir, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// prune removes what the stream did not contain.
func (m *mirror) prune() error {
	return filepath.WalkDir(m.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(m.dir, p)
		if err != nil || rel == "." {
			return err
		}

		rel = filepath.ToSlash(rel)
		if Excluded(rel, d.IsDir(), m.o.Exclude) {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if m.seen[rel] {
			return nil
		}

		if err := os.RemoveAll(p); err != nil {
			return err
		}

		if d.IsDir() {
			return filepath.SkipDir
		}

		return nil
	})
}

// cleanRel turns a tar name into a clean relative path, refusing absolute paths and "..".
func cleanRel(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") {
		return "", ErrOutside
	}

	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == ".." {
			return "", ErrOutside
		}
	}

	return path.Clean(name), nil
}
