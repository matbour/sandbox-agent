package guestexec

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, body, link string
	dir              bool
}

func tarOf(t *testing.T, entries ...entry) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}

		switch {
		case e.dir:
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		case e.link != "":
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, e.link, 0
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return &buf
}

func write(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorRoundTrip(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()

	write(t, filepath.Join(src, "a.txt"), "changed")
	write(t, filepath.Join(src, "new/b.txt"), "new")
	write(t, filepath.Join(src, "node_modules/x/index.js"), "dep")
	write(t, filepath.Join(src, "run.sh"), "#!/bin/sh")

	if err := os.Chmod(filepath.Join(src, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(dst, "a.txt"), "original")
	write(t, filepath.Join(dst, "deleted.txt"), "gone in the guest")
	write(t, filepath.Join(dst, "old/c.txt"), "gone too")
	write(t, filepath.Join(dst, ".cache/keep"), "excluded, left alone")

	exclude := []string{"node_modules/", ".cache/"}

	var buf bytes.Buffer
	if err := WriteTree(&buf, src, TreeOptions{Exclude: exclude}); err != nil {
		t.Fatal(err)
	}

	if err := Mirror(dst, &buf, MirrorOptions{Exclude: exclude}); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]string{"a.txt": "changed", "new/b.txt": "new", ".cache/keep": "excluded, left alone"} {
		if b, err := os.ReadFile(filepath.Join(dst, p)); err != nil || string(b) != want {
			t.Errorf("%s: %q, %v; want %q", p, b, err, want)
		}
	}

	for _, p := range []string{"deleted.txt", "old", "node_modules"} {
		if _, err := os.Lstat(filepath.Join(dst, p)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should not exist: %v", p, err)
		}
	}

	if fi, err := os.Stat(filepath.Join(dst, "run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode: %v %v", fi, err)
	}

	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "a.txt" {
		t.Errorf("link: %q %v", l, err)
	}
}

// A hostile guest must not write outside the workspace (E-OUT for the Firecracker workspace).
func TestMirrorRefusesEscapes(t *testing.T) {
	cases := map[string][]entry{
		"dot-dot":             {{name: "../escaped", body: "x"}},
		"nested dot-dot":      {{name: "a/../../escaped", body: "x"}},
		"absolute":            {{name: "/tmp/escaped", body: "x"}},
		"through a symlink":   {{name: "out", link: "OUTSIDE"}, {name: "out/escaped", body: "x"}},
		"symlink to a parent": {{name: "up", link: ".."}, {name: "up/escaped", body: "x"}},
	}

	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "outside")

			for _, d := range []string{ws, outside} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			for i := range entries {
				entries[i].link = strings.ReplaceAll(entries[i].link, "OUTSIDE", outside)
			}

			err := Mirror(ws, tarOf(t, entries...), MirrorOptions{})
			if err == nil {
				t.Fatal("hostile stream accepted")
			}

			for _, p := range []string{filepath.Join(base, "escaped"), filepath.Join(outside, "escaped"), "/tmp/escaped"} {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s was written", p)
				}
			}
		})
	}
}

func TestMirrorLimits(t *testing.T) {
	err := Mirror(t.TempDir(), tarOf(t, entry{name: "big", body: strings.Repeat("x", 100)}), MirrorOptions{MaxBytes: 10})
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("size limit not enforced: %v", err)
	}

	err = Mirror(t.TempDir(), tarOf(t, entry{name: "a"}, entry{name: "b"}, entry{name: "c"}), MirrorOptions{MaxEntries: 2})
	if err == nil || !strings.Contains(err.Error(), "entries") {
		t.Errorf("entry limit not enforced: %v", err)
	}
}

func TestExcluded(t *testing.T) {
	p := []string{"node_modules/", "*.pyc"}

	for rel, want := range map[string]bool{
		"node_modules":         true,
		"pkg/node_modules":     true,
		"a/b.pyc":              true,
		"node_modules.txt":     false,
		"src/main.go":          false,
		"node_modules_but_dir": false,
	} {
		if got := Excluded(rel, !strings.Contains(rel, "."), p); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestFrames(t *testing.T) {
	var buf bytes.Buffer

	big := bytes.Repeat([]byte("y"), MaxFrame+10)

	for _, err := range []error{
		WriteFrame(&buf, Stdout, []byte("out")),
		WriteFrame(&buf, Stderr, big),
		WriteEnd(&buf, Result{ExitCode: 3}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	var so, se bytes.Buffer

	res, err := ReadFrames(&buf, map[byte]io.Writer{Stdout: &so, Stderr: &se})
	if err != nil || res.ExitCode != 3 || so.String() != "out" || se.Len() != len(big) {
		t.Errorf("res %+v err %v stdout %q stderr %d", res, err, so.String(), se.Len())
	}

	var bad bytes.Buffer

	_ = WriteFrame(&bad, Data, []byte("not asked for"))
	if _, err := ReadFrames(&bad, map[byte]io.Writer{Stdout: &so}); err == nil {
		t.Error("unexpected frame kind accepted")
	}
}
