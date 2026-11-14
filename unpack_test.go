package audiobookfetch

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// makeZip writes a zip holding the given names; a name ending in / is a
// folder entry.
func makeZip(t *testing.T, path string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(files[name]))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// under lists the files below dir, relative to it.
func under(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestUnpackZip(t *testing.T) {
	library := t.TempDir()
	book := filepath.Join(library, "Some Book")
	archive := makeZip(t, filepath.Join(book, "Some Book.zip"), map[string]string{
		"0123abcd/":                   "",
		"Some Book/01.mp3":            "one",
		"Some Book/disc 2/02.mp3":     "two",
		"Some Book/what?.jpg":         "cover",
		"__MACOSX/Some Book/._01.mp3": "junk",
		"Some Book/.DS_Store":         "junk",
		"../../escaped.txt":           "climbs out",
		"/absolute.txt":               "absolute",
		`..\..\windows-escape.txt`:    "climbs out, the other way",
	})
	cover := filepath.Join(book, "cover.jpg")
	if err := os.WriteFile(cover, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, unpacked, notes := (&Unpacker{}).Unpack([]string{cover, archive})
	if len(notes) != 1 || notes[0] != "unpacked Some Book.zip: 6 files" {
		t.Errorf("notes: %q", notes)
	}
	want := []string{"Some Book/01.mp3", "Some Book/disc 2/02.mp3", "Some Book/what_.jpg", "absolute.txt", "cover.jpg",
		"escaped.txt", "windows-escape.txt"}
	if got := under(t, book); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the book's folder now holds:\n%q\nwant:\n%q", got, want)
	}
	if got := under(t, library); len(got) != len(want) {
		t.Errorf("nothing may land outside the book's folder: %q", got)
	}
	if len(files) != 7 || files[0] != cover || len(unpacked[archive]) != 6 {
		t.Errorf("files %q, unpacked %q", files, unpacked)
	}
	for _, path := range files {
		if path == archive {
			t.Error("the zip is gone, so it must not be listed")
		}
	}
	if data, _ := os.ReadFile(filepath.Join(book, "Some Book", "disc 2", "02.mp3")); string(data) != "two" {
		t.Errorf("unpacked file holds %q", data)
	}
}

func TestUnpackLeavesAZipItCannotTrustExactlyAsItWas(t *testing.T) {
	check := func(name string, prepare func(book string) string, wantNote string) {
		t.Helper()
		book := filepath.Join(t.TempDir(), "Book")
		archive := prepare(book)
		before := under(t, book)
		files, unpacked, notes := (&Unpacker{}).Unpack([]string{archive})
		if len(notes) != 1 || !strings.Contains(notes[0], "could not unpack") || !strings.Contains(notes[0], wantNote) {
			t.Errorf("%s: notes %q", name, notes)
		}
		if len(unpacked) != 0 || len(files) != 1 || files[0] != archive {
			t.Errorf("%s: the archive stays in the list: %q %q", name, files, unpacked)
		}
		if after := under(t, book); strings.Join(after, "|") != strings.Join(before, "|") {
			t.Errorf("%s: the folder changed: %q -> %q", name, before, after)
		}
	}

	check("a bomb", func(book string) string {
		old := unpackSlack
		unpackSlack = 1 << 10
		t.Cleanup(func() { unpackSlack = old })
		return makeZip(t, filepath.Join(book, "bomb.zip"), map[string]string{
			"a.mp3": "small", "b.mp3": strings.Repeat("\x00", 8<<20)})
	}, "far more than its size")

	check("a clash", func(book string) string {
		archive := makeZip(t, filepath.Join(book, "book.zip"), map[string]string{"01.mp3": "new", "02.mp3": "different length"})
		_ = os.WriteFile(filepath.Join(book, "02.mp3"), []byte("mine"), 0o644)
		return archive
	}, "would overwrite 02.mp3")

	check("not a zip at all", func(book string) string {
		_ = os.MkdirAll(book, 0o755)
		path := filepath.Join(book, "fake.zip")
		_ = os.WriteFile(path, []byte("this is not a zip"), 0o644)
		return path
	}, "zip")

	check("empty", func(book string) string {
		return makeZip(t, filepath.Join(book, "empty.zip"), map[string]string{"folder/": ""})
	}, "no files in it")

	check("password-protected", func(book string) string {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		entry, _ := w.CreateRaw(&zip.FileHeader{Name: "01.mp3", Flags: 0x1, Method: zip.Store, UncompressedSize64: 3, CompressedSize64: 3})
		_, _ = entry.Write([]byte("xxx"))
		_ = w.Close()
		_ = os.MkdirAll(book, 0o755)
		path := filepath.Join(book, "locked.zip")
		_ = os.WriteFile(path, buf.Bytes(), 0o644)
		return path
	}, "password-protected")
}

func TestUnpackSkipsFilesAlreadyThere(t *testing.T) {
	book := filepath.Join(t.TempDir(), "Book")
	archive := makeZip(t, filepath.Join(book, "book.zip"), map[string]string{"01.mp3": "one", "02.mp3": "two"})
	_ = os.WriteFile(filepath.Join(book, "01.mp3"), []byte("ONE"), 0o644) // same size: taken to be the same file
	files, _, notes := (&Unpacker{}).Unpack([]string{archive})
	if len(files) != 2 || !strings.Contains(notes[0], "unpacked book.zip: 2 files") {
		t.Fatalf("files %q, notes %q", files, notes)
	}
	if data, _ := os.ReadFile(filepath.Join(book, "01.mp3")); string(data) != "ONE" {
		t.Errorf("a file already there is never overwritten, got %q", data)
	}
}

// fakeArchiver stands in for 7zz, unar and bsdtar: it "unpacks" by writing
// files into the scratch folder it is pointed at.
type fakeArchiver struct {
	contents map[string]string // name inside the archive -> content
	fail     map[string]bool   // programs that cannot read the archive
	outside  string            // a file a link in the archive points at
	calls    []string
}

func (f *fakeArchiver) run(name string, args ...string) error {
	f.calls = append(f.calls, name)
	if f.fail[name] {
		return errors.New(name + ": unsupported archive")
	}
	into := ""
	for i, arg := range args {
		switch {
		case strings.HasPrefix(arg, "-o"):
			into = strings.TrimPrefix(arg, "-o")
		case arg == "-C" || arg == "-output-directory":
			into = args[i+1]
		}
	}
	for name, content := range f.contents {
		path := filepath.Join(into, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	if f.outside != "" {
		return os.Symlink(f.outside, filepath.Join(into, "link.mp3"))
	}
	return nil
}

func touch(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("archive bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnpackRarAnd7zThroughAnOutsideProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a symbolic link")
	}
	library := t.TempDir()
	book := filepath.Join(library, "Book")
	secret := filepath.Join(library, "secret.txt")
	_ = os.WriteFile(secret, []byte("not part of the book"), 0o644)
	first, second, third := filepath.Join(book, "book.part1.rar"), filepath.Join(book, "book.part2.rar"), filepath.Join(book, "book.part03.rar")
	other, piece := filepath.Join(book, "extras.rar"), filepath.Join(book, "extras.r00")
	sevenZip := filepath.Join(book, "more.7z")
	touch(t, first, second, third, other, piece, sevenZip)

	tools := &fakeArchiver{contents: map[string]string{"Book/01.mp3": "one", "Book/sub/02.mp3": "two"},
		fail: map[string]bool{"7zz": true}, outside: secret}
	u := &Unpacker{SevenZip: "7zz", BSDTar: "bsdtar", run: tools.run}
	files, unpacked, notes := u.Unpack([]string{first, second, third, other, piece, sevenZip})

	// The first program could not read them, so the next one was tried:
	// once per archive, never for the later pieces of one.
	if got := strings.Join(tools.calls, " "); got != "7zz bsdtar 7zz bsdtar 7zz bsdtar" {
		t.Errorf("programs run: %s", got)
	}
	if got := under(t, book); strings.Join(got, "|") != "Book/01.mp3|Book/sub/02.mp3" {
		t.Errorf("every piece of every archive is gone, and only plain files came out (no link, no scratch folder): %q", got)
	}
	if len(files) != 2 || len(unpacked[first]) != 2 || len(unpacked[second]) != 2 || len(unpacked[piece]) != 2 || len(notes) != 3 {
		t.Errorf("files %q\nunpacked %q\nnotes %q", files, unpacked, notes)
	}
	if data, _ := os.ReadFile(secret); string(data) != "not part of the book" {
		t.Error("a file a link pointed at was touched")
	}
}

func TestUnpackSaysWhenNoProgramCanOpenAnArchive(t *testing.T) {
	book := filepath.Join(t.TempDir(), "Book")
	archive := filepath.Join(book, "book.rar")
	touch(t, archive)

	u := &Unpacker{}
	if missing := u.Missing(); len(missing) != 1 || !strings.Contains(missing[0], "rar and 7z") {
		t.Errorf("missing: %q", missing)
	}
	files, unpacked, notes := u.Unpack([]string{archive})
	if len(files) != 1 || files[0] != archive || len(unpacked) != 0 || !strings.Contains(notes[0], "none of 7zz, 7z, unar or bsdtar is installed") {
		t.Errorf("files %q, notes %q", files, notes)
	}

	// A program that fails (a password, a damaged file) leaves it alone too.
	tools := &fakeArchiver{fail: map[string]bool{"unar": true}}
	u = &Unpacker{Unar: "unar", run: tools.run}
	if len(u.Missing()) != 0 {
		t.Error("with a program installed nothing is missing")
	}
	if _, _, notes = u.Unpack([]string{archive}); !strings.Contains(notes[0], "could not unpack book.rar: unar: unsupported archive") {
		t.Errorf("notes %q", notes)
	}
	if got := under(t, book); strings.Join(got, "|") != "book.rar" {
		t.Errorf("the folder should be as it was: %q", got)
	}
}

// With a real bsdtar on the machine, a real 7z archive goes through it.
func TestUnpackARealSevenZipArchive(t *testing.T) {
	bsdtar, err := exec.LookPath("bsdtar")
	if err != nil {
		t.Skip("bsdtar is not installed")
	}
	source := t.TempDir()
	touch(t, filepath.Join(source, "Book", "01.mp3"), filepath.Join(source, "Book", "sub", "02.mp3"))
	book := filepath.Join(t.TempDir(), "Book")
	_ = os.MkdirAll(book, 0o755)
	archive := filepath.Join(book, "book.7z")
	if out, err := exec.Command(bsdtar, "-c", "--format", "7zip", "-f", archive, "-C", source, "Book").CombinedOutput(); err != nil {
		t.Skipf("this bsdtar cannot write 7z: %v %s", err, out)
	}

	files, _, notes := NewUnpacker().Unpack([]string{archive})
	if len(files) != 2 || !strings.Contains(notes[0], "unpacked book.7z: 2 files") {
		t.Fatalf("files %q, notes %q", files, notes)
	}
	if got := under(t, book); strings.Join(got, "|") != "Book/01.mp3|Book/sub/02.mp3" {
		t.Errorf("the folder holds %q", got)
	}
}
