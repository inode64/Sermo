package checks

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"sermo/internal/hostfs"
	"sermo/internal/units"
)

const (
	javaArchiveMaxCount  = 256
	javaManifestMaxBytes = 64 * units.BytesPerKiB
	javaArchiveMaxBytes  = 512 * units.BytesPerMiB
	javaInflatedMaxBytes = units.BytesPerGiB
	javaCRCBufferBytes   = 32 * units.BytesPerKiB
	javaManifestPath     = "META-INF/MANIFEST.MF"
	javaClassPath        = "class-path"
	javaUntrustedMode    = 0o022
)

func inspectJavaArchives(ctx context.Context, roots []string, open func(string) (*os.File, error)) ([]string, error) {
	if len(roots) == 0 || len(roots) > javaArchiveMaxCount {
		return nil, fmt.Errorf("%w: invalid Java archive root count", errConfigSyntax)
	}
	required := make(map[string]bool)
	for _, path := range roots {
		if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".jar") {
			return nil, fmt.Errorf("%w: Java roots must be absolute JAR paths", errConfigSyntax)
		}
		required[filepath.Clean(path)] = true
	}
	pending := slices.Sorted(maps.Keys(required))
	seen := maps.Clone(required)
	var inflated uint64
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("inspect Java archives: %w", err)
		}
		path := pending[0]
		pending = pending[1:]
		references, err := inspectJavaArchive(ctx, path, open, &inflated)
		if errors.Is(err, os.ErrNotExist) && !required[path] {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("java archive %s: %w", path, err)
		}
		for _, reference := range references {
			child, err := javaArchiveReference(path, reference)
			if err != nil {
				return nil, err
			}
			if seen[child] {
				continue
			}
			if len(seen) >= javaArchiveMaxCount {
				return nil, fmt.Errorf("%w: Java dependency count exceeds limit", errConfigSyntax)
			}
			seen[child] = true
			pending = append(pending, child)
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

func inspectJavaArchive(ctx context.Context, path string, open func(string) (*os.File, error), inflated *uint64) ([]string, error) {
	file, err := open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat archive: %w", err)
	}
	if info.Size() > javaArchiveMaxBytes {
		return nil, fmt.Errorf("%w: Java archive exceeds size limit", errConfigSyntax)
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigSyntax, err)
	}
	var manifest *zip.File
	for _, entry := range archive.File {
		if entry.UncompressedSize64 > javaInflatedMaxBytes-*inflated {
			return nil, fmt.Errorf("%w: Java expanded data exceeds limit", errConfigSyntax)
		}
		*inflated += entry.UncompressedSize64
		if err := verifyJavaEntry(ctx, entry); err != nil {
			return nil, err
		}
		if entry.Name == javaManifestPath {
			manifest = entry
		}
	}
	if manifest == nil {
		return nil, nil
	}
	stream, err := manifest.Open()
	if err != nil {
		return nil, fmt.Errorf("open Java manifest: %w", err)
	}
	defer func() { _ = stream.Close() }()
	data, err := io.ReadAll(io.LimitReader(stream, javaManifestMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Java manifest: %w", err)
	}
	return javaManifestClasspath(data)
}

func verifyJavaEntry(ctx context.Context, entry *zip.File) error {
	stream, err := entry.Open()
	if err != nil {
		return fmt.Errorf("%w: open ZIP member: %w", errConfigSyntax, err)
	}
	defer func() { _ = stream.Close() }()
	reader := io.LimitReader(stream, javaInflatedMaxBytes+1)
	buffer := make([]byte, javaCRCBufferBytes)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("verify Java CRC: %w", err)
		}
		n, err := reader.Read(buffer)
		read += int64(n)
		if read > javaInflatedMaxBytes {
			return fmt.Errorf("%w: ZIP member exceeds expanded size limit", errConfigSyntax)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: ZIP member CRC/read failure: %w", errConfigSyntax, err)
		}
	}
}

func javaManifestClasspath(data []byte) ([]string, error) {
	main := string(data)
	for _, separator := range []string{"\r\n\r\n", "\n\n", "\r\r"} {
		main, _, _ = strings.Cut(main, separator)
	}
	// Signed archives can carry large per-member sections; only the main
	// section controls Class-Path. All sections still pass CRC verification.
	if len(main) > javaManifestMaxBytes || !utf8.ValidString(main) {
		return nil, fmt.Errorf("%w: Java manifest main section is oversized or invalid UTF-8", errConfigSyntax)
	}
	main = strings.ReplaceAll(strings.ReplaceAll(main, "\r\n", "\n"), "\r", "\n")
	var headers []string
	for line := range strings.SplitSeq(main, "\n") {
		if strings.HasPrefix(line, " ") {
			if len(headers) == 0 {
				return nil, fmt.Errorf("%w: orphan Java manifest continuation", errConfigSyntax)
			}
			headers[len(headers)-1] += line[1:]
		} else {
			headers = append(headers, line)
		}
	}
	var found bool
	var references []string
	for _, header := range headers {
		name, value, ok := strings.Cut(header, ":")
		if !strings.EqualFold(name, javaClassPath) {
			continue
		}
		if !ok || found {
			return nil, fmt.Errorf("%w: duplicate or malformed Java Class-Path", errConfigSyntax)
		}
		found = true
		references = strings.Fields(value)
	}
	return references, nil
}

func javaArchiveReference(path, reference string) (string, error) {
	ref, err := url.Parse(reference)
	if err != nil {
		return "", fmt.Errorf("%w: Java dependency URL: %w", errConfigSyntax, err)
	}
	base := url.URL{Scheme: "file", Path: path}
	target := base.ResolveReference(ref)
	if target.Scheme != "file" || (target.Host != "" && target.Host != "localhost") || target.User != nil || target.Opaque != "" || target.RawQuery != "" || target.Fragment != "" || !filepath.IsAbs(target.Path) || !strings.EqualFold(filepath.Ext(target.Path), ".jar") {
		return "", fmt.Errorf("%w: Java dependency must name a local JAR", errConfigSyntax)
	}
	return filepath.Clean(target.Path), nil
}

func openTrustedJavaArchive(path string) (*os.File, error) {
	resolved, err := hostfs.EvalSymlinksTarget(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("resolve Java archive: %w", err)
		}
		if err := trustedMissingJavaPath(path); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("java archive %s: %w", path, os.ErrNotExist)
	}
	if err := trustedJavaParents(filepath.Dir(resolved)); err != nil {
		return nil, err
	}
	file, err := hostfs.OpenFile(resolved, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open Java archive: %w", err)
	}
	info, err := file.Stat()
	if err == nil {
		err = trustedJavaMetadata(info, false)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("trust Java archive %s: %w", path, err)
	}
	return file, nil
}

func trustedMissingJavaPath(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		if _, err := hostfs.LstatTarget(current); err == nil {
			resolved, err := hostfs.EvalSymlinksTarget(current)
			if err != nil {
				return fmt.Errorf("resolve missing Java dependency parent: %w", err)
			}
			return trustedJavaParents(resolved)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect missing Java dependency parent: %w", err)
		}
		if current == filepath.Dir(current) {
			return errors.New("java dependency has no verifiable parent")
		}
	}
}

func trustedJavaParents(path string) error {
	for {
		info, err := hostfs.StatTarget(path)
		if err != nil {
			return fmt.Errorf("stat Java archive parent: %w", err)
		}
		if err := trustedJavaMetadata(info, true); err != nil {
			return fmt.Errorf("java archive parent %s: %w", path, err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func trustedJavaMetadata(info os.FileInfo, directory bool) error {
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || info.Mode().Perm()&javaUntrustedMode != 0 || info.IsDir() != directory || (!directory && (!info.Mode().IsRegular() || info.Size() == 0)) {
		return fmt.Errorf("%w: Java archives and parents must be root-owned, non-writable by group/others and regular files/directories", errConfigSyntax)
	}
	return nil
}
