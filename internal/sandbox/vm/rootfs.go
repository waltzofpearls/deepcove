package vm

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/ext4fs"
	"github.com/waltzofpearls/deepcove/internal/guestinit"
)

// xattrPAXPrefix is the PAX record prefix GNU/BSD tar uses to carry extended attributes. Container
// images ship file capabilities (e.g. security.capability on ping) this way.
const xattrPAXPrefix = "SCHILY.xattr."

// buildStats reports what a rootfs build contained, for logging and tests.
type buildStats struct {
	Files     int
	Dirs      int
	Symlinks  int
	Hardlinks int
	Devices   int // char + block device nodes + FIFOs
}

// injectedFile is a host file to bake into the rootfs at a fixed guest path, on top of the OCI content.
// The guest init binary and its launch script ride in this way so the image boots itself.
type injectedFile struct {
	Target string // absolute guest path, e.g. /sbin/deepcove-init
	Source string // host path to copy bytes from
	Mode   uint16 // guest file mode, e.g. 0o755
}

// injectedDir is an empty directory to bake into the rootfs. The guest init needs its overlay mount
// point to exist before boot: the base is mounted read only, so init cannot create one at runtime.
type injectedDir struct {
	Target string
	Mode   uint16
}

// injection is everything baked into the image on top of the OCI content. Dirs are created before Files
// so a file can land inside an injected directory.
type injection struct {
	Dirs  []injectedDir
	Files []injectedFile
}

// writeRootfs streams a FLATTENED OCI filesystem tar into a fresh read only ext4 image on w, then bakes
// in the injected files. Flattened means layers already merged with whiteouts resolved (what
// `crane export` / `docker export` produce), so there are no `.wh.` entries to handle here. The image is
// read only by construction (the engine sets RoCompatReadonly); a session's writes go to the overlay in
// the guest.
//
// This mirrors hcsshim's ConvertTarToExt4 walk, minus the whiteout branch we don't need, over the
// vendored compactext4 engine. compactext4 writes uid/gid/mode/devnodes/symlinks/hardlinks/xattrs
// straight from the tar headers, so it never stages files on the macOS host and is immune to the
// APFS case folding and unprivileged uid 0 problems a host directory extract would hit.
func writeRootfs(w io.ReadWriteSeeker, tr *tar.Reader, inject injection) (buildStats, error) {
	var stats buildStats
	fs := ext4fs.NewWriter(w)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("read tar entry: %w", err)
		}
		if err := fs.MakeParents(hdr.Name); err != nil {
			return stats, fmt.Errorf("%s: make parents: %w", hdr.Name, err)
		}
		if err := writeEntry(fs, tr, hdr, &stats); err != nil {
			return stats, fmt.Errorf("%s: %w", hdr.Name, err)
		}
	}
	for _, d := range inject.Dirs {
		if err := injectDir(fs, d, &stats); err != nil {
			return stats, fmt.Errorf("inject dir %s: %w", d.Target, err)
		}
	}
	for _, f := range inject.Files {
		if err := injectFile(fs, f, &stats); err != nil {
			return stats, fmt.Errorf("inject %s: %w", f.Target, err)
		}
	}
	if err := fs.Close(); err != nil {
		return stats, fmt.Errorf("finalize ext4: %w", err)
	}
	return stats, nil
}

// baseInjection is what every session image needs regardless of which session uses it: the guest init
// binary, and the overlay mount point (which must be baked in because the base is mounted read only, so
// init has nowhere to mkdir until its tmpfs exists).
//
// Deliberately nothing per session. The base image is cached per image digest and shared by every
// session using that image, so anything session specific (the entrypoint, the agent) is served over
// vsock at boot instead. See internal/guestcfg.
func baseInjection(initBinary string) injection {
	return injection{
		Dirs: []injectedDir{
			{Target: guestinit.OverlayMountDir, Mode: 0o755},
		},
		Files: []injectedFile{
			{Target: guestinit.InitPath, Source: initBinary, Mode: 0o755},
		},
	}
}

// injectDir creates an empty directory in the image, replacing any OCI entry at that path.
func injectDir(fs *ext4fs.Writer, d injectedDir, stats *buildStats) error {
	if err := fs.MakeParents(d.Target); err != nil {
		return fmt.Errorf("make parents: %w", err)
	}
	if err := fs.Create(d.Target, &ext4fs.File{Mode: d.Mode | ext4fs.S_IFDIR}); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	stats.Dirs++
	return nil
}

// injectFile copies a host file into the image at f.Target, owned by root, replacing any OCI entry at
// that path. Its parents are created if the base image lacks them.
func injectFile(fs *ext4fs.Writer, f injectedFile, stats *buildStats) error {
	src, err := os.Open(f.Source)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if err := fs.MakeParents(f.Target); err != nil {
		return fmt.Errorf("make parents: %w", err)
	}
	if err := fs.Create(f.Target, &ext4fs.File{Mode: f.Mode | ext4fs.S_IFREG, Size: info.Size()}); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if _, err := io.Copy(fs, src); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	stats.Files++
	return nil
}

func writeEntry(fs *ext4fs.Writer, tr io.Reader, hdr *tar.Header, stats *buildStats) error {
	if hdr.Typeflag == tar.TypeLink {
		if err := fs.Link(hdr.Linkname, hdr.Name); err != nil {
			return fmt.Errorf("hardlink to %s: %w", hdr.Linkname, err)
		}
		stats.Hardlinks++
		return nil
	}

	typ, ok := fileType(hdr.Typeflag)
	if !ok {
		return nil // pax headers and other non file entries carry no rootfs content
	}

	f := &ext4fs.File{
		Mode:     (uint16(hdr.Mode) & ^ext4fs.TypeMask) | typ,
		Atime:    hdr.AccessTime,
		Mtime:    hdr.ModTime,
		Ctime:    hdr.ChangeTime,
		Crtime:   hdr.ModTime,
		Size:     hdr.Size,
		Uid:      uint32(hdr.Uid),
		Gid:      uint32(hdr.Gid),
		Linkname: hdr.Linkname,
		Devmajor: uint32(hdr.Devmajor),
		Devminor: uint32(hdr.Devminor),
		Xattrs:   paxXattrs(hdr.PAXRecords),
	}
	if err := fs.Create(hdr.Name, f); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if typ == ext4fs.S_IFREG {
		if _, err := io.Copy(fs, tr); err != nil {
			return fmt.Errorf("write contents: %w", err)
		}
	}
	countEntry(typ, stats)
	return nil
}

// fileType maps a tar type flag to the ext4 mode type bits, reporting false for entries that carry no
// filesystem object (PAX/global headers).
func fileType(flag byte) (uint16, bool) {
	switch flag {
	case tar.TypeReg:
		return ext4fs.S_IFREG, true
	case tar.TypeSymlink:
		return ext4fs.S_IFLNK, true
	case tar.TypeChar:
		return ext4fs.S_IFCHR, true
	case tar.TypeBlock:
		return ext4fs.S_IFBLK, true
	case tar.TypeDir:
		return ext4fs.S_IFDIR, true
	case tar.TypeFifo:
		return ext4fs.S_IFIFO, true
	default:
		return 0, false
	}
}

func countEntry(typ uint16, stats *buildStats) {
	switch typ {
	case ext4fs.S_IFREG:
		stats.Files++
	case ext4fs.S_IFDIR:
		stats.Dirs++
	case ext4fs.S_IFLNK:
		stats.Symlinks++
	case ext4fs.S_IFCHR, ext4fs.S_IFBLK, ext4fs.S_IFIFO:
		stats.Devices++
	}
}

func paxXattrs(records map[string]string) map[string][]byte {
	var xattrs map[string][]byte
	for key, value := range records {
		name, ok := strings.CutPrefix(key, xattrPAXPrefix)
		if !ok {
			continue
		}
		if xattrs == nil {
			xattrs = make(map[string][]byte)
		}
		xattrs[name] = []byte(value)
	}
	return xattrs
}

// createRootfs builds a read only ext4 rootfs image at imagePath from a flattened OCI tar, baking in any
// injected files (the guest init binary and its launch script).
func createRootfs(imagePath string, tr *tar.Reader, inject injection) (buildStats, error) {
	f, err := os.Create(imagePath)
	if err != nil {
		return buildStats{}, fmt.Errorf("create image file: %w", err)
	}
	defer func() { _ = f.Close() }()
	stats, err := writeRootfs(f, tr, inject)
	if err != nil {
		return stats, err
	}
	if err := f.Close(); err != nil {
		return stats, fmt.Errorf("close image file: %w", err)
	}
	return stats, nil
}
