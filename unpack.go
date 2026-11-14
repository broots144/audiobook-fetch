package audiobookfetch

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Unpacking: a download that arrives as an archive -> the files inside it.
//
// Some listings wrap the whole book in a .zip, .rar or .7z. Left like that,
// the check finds no audio and a good book is thrown out, so archives are
// opened first and, once everything inside is safely on disk, deleted.
//
// Zip is read here. Rar and 7z are handed to an outside program, whichever
// of 7-Zip, unar or bsdtar is installed; with none, those archives are left
// as they are and said so. Either way an archive is untrusted: nothing in it
// can be written outside the folder the archive is in.

const (
	maxUnpackEntries = 50000
	// An archive may not grow beyond this many times its own size (plus
	// some slack for tiny ones). Audio barely compresses; a file that
	// expands a thousandfold is a bomb, not a book.
	maxUnpackRatio = 200
	unpackTimeout  = 30 * time.Minute
)

// unpackSlack is a variable so that the tests can make a small bomb.
var unpackSlack int64 = 64 << 20

var (
	archiveExtensions = map[string]bool{".zip": true, ".rar": true, ".7z": true}
	// "book.part02.rar" and "book.r00" are the later pieces of a rar that
	// starts in "book.part01.rar" or "book.rar".
	rarPart    = regexp.MustCompile(`(?i)^(.*)\.part0*(\d+)\.rar$`)
	rarOldPart = regexp.MustCompile(`(?i)^(.*)\.[r-z]\d\d$`)
)

// IsArchive reports whether path is named like an archive Unpack opens.
func IsArchive(path string) bool { return archiveExtensions[strings.ToLower(filepath.Ext(path))] }

// Unpacker opens archives. The zero value handles zip only; use NewUnpacker,
// which finds the programs for the other kinds.
type Unpacker struct {
	SevenZip string // path to 7zz or 7z; "" if not installed
	Unar     string // path to unar
	BSDTar   string // path to bsdtar

	run func(name string, args ...string) error
}

// NewUnpacker returns an Unpacker using whichever programs are on the PATH.
func NewUnpacker() *Unpacker {
	find := func(names ...string) string {
		for _, name := range names {
			if path, err := exec.LookPath(name); err == nil {
				return path
			}
		}
		return ""
	}
	return &Unpacker{SevenZip: find("7zz", "7z"), Unar: find("unar"), BSDTar: find("bsdtar")}
}

// Missing says, in plain words, what cannot be unpacked on this machine.
func (u *Unpacker) Missing() []string {
	if len(u.tools()) == 0 {
		return []string{"rar and 7z archives cannot be unpacked: none of 7zz, 7z, unar or bsdtar is installed"}
	}
	return nil
}

// tool is one way to run an outside unpacker.
type tool struct {
	path string
	args func(archive, into string) []string
}

func (u *Unpacker) tools() []tool {
	var out []tool
	if u.SevenZip != "" {
		out = append(out, tool{u.SevenZip, func(archive, into string) []string {
			return []string{"x", "-y", "-bd", "-o" + into, "--", archive}
		}})
	}
	if u.Unar != "" {
		out = append(out, tool{u.Unar, func(archive, into string) []string {
			return []string{"-quiet", "-force-overwrite", "-no-directory", "-output-directory", into, archive}
		}})
	}
	if u.BSDTar != "" {
		out = append(out, tool{u.BSDTar, func(archive, into string) []string {
			return []string{"-x", "-f", archive, "-C", into}
		}})
	}
	return out
}

func (u *Unpacker) exec(name string, args ...string) error {
	if u.run != nil {
		return u.run(name, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), unpackTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr // no stdin: a prompt for a password fails at once
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return fmt.Errorf("%s: %s", filepath.Base(name), last)
		}
		return fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return nil
}

// Unpack opens every archive among paths, each into the folder it is in,
// and deletes the archives that came out whole. It returns the files as
// they now stand (archives replaced by their contents), which archive
// became which files, and a line for each archive saying what happened.
// An archive that cannot be unpacked is left exactly as it was.
func (u *Unpacker) Unpack(paths []string) (files []string, unpacked map[string][]string, notes []string) {
	unpacked = map[string][]string{}
	gone := map[string]bool{}
	for _, path := range paths {
		if !IsArchive(path) || laterRarPart(path) {
			continue
		}
		inside, err := u.extract(path)
		if err != nil {
			notes = append(notes, fmt.Sprintf("could not unpack %s: %v", filepath.Base(path), err))
			continue
		}
		pieces := append([]string{path}, rarPieces(path, paths)...)
		for _, piece := range pieces {
			if err := os.Remove(piece); err != nil && !errors.Is(err, os.ErrNotExist) {
				notes = append(notes, fmt.Sprintf("unpacked %s but could not delete it: %v", filepath.Base(piece), err))
				continue
			}
			gone[piece] = true
		}
		// Every piece stands for the same contents, so that a later run
		// asks for none of them again.
		for _, piece := range pieces {
			unpacked[piece] = inside
		}
		notes = append(notes, fmt.Sprintf("unpacked %s: %d files", filepath.Base(path), len(inside)))
	}
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] && !gone[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	for _, path := range paths {
		add(path)
		for _, inside := range unpacked[path] {
			add(inside)
		}
	}
	return files, unpacked, notes
}

// laterRarPart reports that path is a piece of a multi-part rar other than
// the first, which is the only one to unpack from.
func laterRarPart(path string) bool {
	m := rarPart.FindStringSubmatch(filepath.Base(path))
	return m != nil && strings.TrimLeft(m[2], "0") != "1"
}

// rarPieces returns the other pieces of the rar that starts at first.
func rarPieces(first string, paths []string) []string {
	if strings.ToLower(filepath.Ext(first)) != ".rar" {
		return nil
	}
	dir, base := filepath.Dir(first), filepath.Base(first)
	var pieces []string
	for _, path := range paths {
		if path == first || filepath.Dir(path) != dir {
			continue
		}
		other := filepath.Base(path)
		if m, n := rarPart.FindStringSubmatch(base), rarPart.FindStringSubmatch(other); m != nil && n != nil && strings.EqualFold(m[1], n[1]) {
			pieces = append(pieces, path)
		} else if n := rarOldPart.FindStringSubmatch(other); m == nil && n != nil && strings.EqualFold(n[1], strings.TrimSuffix(base, filepath.Ext(base))) {
			pieces = append(pieces, path)
		}
	}
	return pieces
}

func (u *Unpacker) extract(archive string) ([]string, error) {
	var files []string
	var err error
	if strings.ToLower(filepath.Ext(archive)) == ".zip" {
		files, err = unzip(archive)
	} else {
		files, err = u.extractWith(archive)
	}
	if err == nil && len(files) == 0 {
		err = errors.New("there are no files in it")
	}
	sort.Strings(files)
	return files, err
}

// unpackTarget maps a name inside an archive to a path under dir, or ""
// for a name that is not worth keeping. Names are cleaned the same way
// download names are, so one cannot climb out of dir.
func unpackTarget(dir, name string) string {
	var parts []string
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "__MACOSX" || part == ".DS_Store" {
			return ""
		}
		if part = cleanSegment(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	path := filepath.Join(append([]string{dir}, parts...)...)
	if rel, err := filepath.Rel(dir, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return path
}

// place decides what to do about target before a file of size bytes is
// written there: nothing (the same file is already there), write it, or
// refuse rather than overwrite something else.
func place(target string, size int64) (write bool, err error) {
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, os.MkdirAll(filepath.Dir(target), 0o755)
	case err != nil:
		return false, err
	case info.Mode().IsRegular() && info.Size() == size:
		return false, nil
	}
	return false, fmt.Errorf("it would overwrite %s", filepath.Base(target))
}

// unzip unpacks a zip into the folder it is in. If anything goes wrong,
// what it wrote is taken away again and the zip is left alone.
func unzip(archive string) (files []string, err error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if len(reader.File) > maxUnpackEntries {
		return nil, fmt.Errorf("it holds %d entries, more than the %d allowed", len(reader.File), maxUnpackEntries)
	}
	budget := unpackSlack
	if info, statErr := os.Stat(archive); statErr == nil {
		budget += info.Size() * maxUnpackRatio
	}

	var written []string
	defer func() {
		if err != nil {
			for _, path := range written {
				_ = os.Remove(path)
			}
			files = nil
		}
	}()
	dir := filepath.Dir(archive)
	for _, entry := range reader.File {
		if !entry.Mode().IsRegular() { // folders, links, devices
			continue
		}
		if entry.Flags&0x1 != 0 {
			return nil, errors.New("it is password-protected")
		}
		target := unpackTarget(dir, entry.Name)
		if target == "" {
			continue
		}
		write, err := place(target, int64(entry.UncompressedSize64))
		if err != nil {
			return nil, err
		}
		if write {
			written = append(written, target)
			n, err := unzipEntry(entry, target, budget)
			if err != nil {
				return nil, err
			}
			budget -= n
		}
		files = append(files, target)
	}
	return files, nil
}

// unzipEntry writes one entry to target, giving up once it passes budget
// bytes whatever size the entry claimed to be.
func unzipEntry(entry *zip.File, target string, budget int64) (int64, error) {
	in, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, budget+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && n > budget {
		err = errors.New("it unpacks to far more than its size")
	}
	return n, err
}

// extractWith has an outside program unpack the archive into a scratch
// folder beside it, then moves the plain files out one by one under cleaned
// names. The program never writes into the book's folder itself, so links
// and odd names in the archive get no further than the scratch folder.
func (u *Unpacker) extractWith(archive string) ([]string, error) {
	tools := u.tools()
	if len(tools) == 0 {
		return nil, errors.New("none of 7zz, 7z, unar or bsdtar is installed")
	}
	dir := filepath.Dir(archive)
	var lastErr error
	for _, tool := range tools {
		files, err := u.extractOnce(tool, archive, dir)
		if err == nil {
			return files, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (u *Unpacker) extractOnce(t tool, archive, dir string) (files []string, err error) {
	scratch, err := os.MkdirTemp(dir, ".unpacking-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if err := u.exec(t.path, t.args(archive, scratch)...); err != nil {
		return nil, err
	}

	// Look before moving anything, so a clash leaves the folder untouched.
	type move struct{ from, to string }
	var moves []move
	err = filepath.WalkDir(scratch, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		if len(moves)+len(files) >= maxUnpackEntries {
			return fmt.Errorf("it holds more than the %d entries allowed", maxUnpackEntries)
		}
		rel, _ := filepath.Rel(scratch, path)
		target := unpackTarget(dir, rel)
		if target == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		write, err := place(target, info.Size())
		if err != nil {
			return err
		}
		if write {
			moves = append(moves, move{path, target})
		} else {
			files = append(files, target)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m.to), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(m.from, m.to); err != nil {
			return nil, err
		}
		files = append(files, m.to)
	}
	return files, nil
}
