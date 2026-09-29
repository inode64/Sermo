package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"sermo/internal/config"
)

const (
	// wizardBackupSuffix names the first backup of the global config; later
	// runs add a timestamp so the operator's original is never overwritten.
	wizardBackupSuffix       = ".bak"
	wizardBackupStampLayout  = "20060102T150405Z"
	wizardBackupMaxAttempts  = 100
	wizardStagingFilePattern = ".*.tmp"
)

// renderConfigPathAppend returns the global config text with relDir appended
// to paths.<pathKey>. It edits the YAML syntax tree so the operator's comments,
// key order and anchors survive, and accepts that rendering only when it
// decodes to exactly want (the map the wizard edited). Anything the syntax edit
// cannot express safely (a flow-style paths mapping, an empty document) falls
// back to re-rendering want, which drops comments but is still correct.
func renderConfigPathAppend(orig []byte, pathKey, relDir string, want map[string]any) ([]byte, error) {
	wantText, err := yaml.Marshal(want)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	if out, ok := appendConfigPathSyntax(orig, pathKey, relDir); ok && sameYAMLDocument(out, wantText) {
		return out, nil
	}
	return wantText, nil
}

// sameYAMLDocument reports whether two YAML texts decode to the same value.
func sameYAMLDocument(a, b []byte) bool {
	var left, right map[string]any
	if yaml.Unmarshal(a, &left) != nil || yaml.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// appendConfigPathSyntax appends relDir to paths.<pathKey> in the parsed
// syntax tree, creating the key or the paths block when missing.
func appendConfigPathSyntax(orig []byte, pathKey, relDir string) ([]byte, bool) {
	file, err := parser.ParseBytes(orig, parser.ParseComments)
	if err != nil || len(file.Docs) != 1 || file.Docs[0].Body == nil {
		return nil, false
	}
	listPath, err := yaml.PathString("$." + config.SectionPaths + "." + pathKey)
	if err != nil {
		return nil, false
	}
	switch node, err := listPath.FilterFile(file); {
	case err == nil && node != nil:
		if !appendSequenceItem(file, listPath, node, relDir) {
			return nil, false
		}
	default:
		if !mergeConfigPathKey(file, pathKey, relDir) {
			return nil, false
		}
	}
	return []byte(file.String()), true
}

// appendSequenceItem adds relDir to an existing list in place, keeping the
// list's style and its item comments; a scalar entry becomes a two-item list.
func appendSequenceItem(file *ast.File, listPath *yaml.Path, node ast.Node, relDir string) bool {
	if seq, isSeq := node.(*ast.SequenceNode); isSeq {
		item, ok := singleItemSequence(relDir)
		if !ok {
			return false
		}
		seq.Values = append(seq.Values, item.Values[0])
		return true
	}
	scalar, isScalar := node.(ast.ScalarNode)
	if !isScalar {
		return false
	}
	current, isString := scalar.GetValue().(string)
	if !isString {
		return false
	}
	replacement, err := yaml.ValueToNode([]string{current, relDir})
	if err != nil {
		return false
	}
	return listPath.ReplaceWithNode(file, replacement) == nil
}

// singleItemSequence renders relDir as a one-item YAML list, so the item is
// quoted exactly as the encoder would quote it.
func singleItemSequence(relDir string) (*ast.SequenceNode, bool) {
	text, err := yaml.Marshal([]string{relDir})
	if err != nil {
		return nil, false
	}
	parsed, err := parser.ParseBytes(text, 0)
	if err != nil || len(parsed.Docs) != 1 {
		return nil, false
	}
	seq, ok := parsed.Docs[0].Body.(*ast.SequenceNode)
	if !ok || len(seq.Values) != 1 {
		return nil, false
	}
	return seq, true
}

// mergeConfigPathKey adds paths.<pathKey>: [relDir] under an existing paths
// block, or a new paths block at the end of the document.
func mergeConfigPathKey(file *ast.File, pathKey, relDir string) bool {
	entry, err := yaml.Marshal(map[string][]string{pathKey: {relDir}})
	if err != nil {
		return false
	}
	pathsPath, err := yaml.PathString("$." + config.SectionPaths)
	if err != nil {
		return false
	}
	if node, err := pathsPath.FilterFile(file); err == nil && node != nil {
		return pathsPath.MergeFromReader(file, bytes.NewReader(entry)) == nil
	}
	block, err := yaml.Marshal(map[string]map[string][]string{config.SectionPaths: {pathKey: {relDir}}})
	if err != nil {
		return false
	}
	rootPath, err := yaml.PathString("$")
	if err != nil {
		return false
	}
	return rootPath.MergeFromReader(file, bytes.NewReader(block)) == nil
}

// writeConfigBackup copies the original config next to it. The first backup
// is <config>.bak; when that already exists a UTC-timestamped name is used,
// so a later run never overwrites the operator's original with a rewritten
// version. Created with O_EXCL: an existing file is never truncated.
func writeConfigBackup(path string, data []byte, mode fs.FileMode) (string, error) {
	first := path + wizardBackupSuffix
	stamp := first + "." + time.Now().UTC().Format(wizardBackupStampLayout)
	for attempt := range wizardBackupMaxAttempts {
		bak := first
		switch {
		case attempt == 1:
			bak = stamp
		case attempt > 1:
			bak = stamp + "." + strconv.Itoa(attempt-1)
		}
		f, err := os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // G304: backup path derives from the operator-selected global config
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create backup %s: %w", bak, err)
		}
		if err := writeAndSync(f, data); err != nil {
			_ = os.Remove(bak)
			return "", fmt.Errorf("write backup %s: %w", bak, err)
		}
		return bak, nil
	}
	return "", fmt.Errorf("no free backup name for %s", path)
}

// replaceFileAtomic replaces path with data through a synced staging file in
// the same directory, keeping the original's permissions and, when allowed,
// its owner and group.
func replaceFileAtomic(path string, data []byte, orig fs.FileInfo) error {
	return stageAndRename(path, data, orig.Mode().Perm(), func(tmp *os.File) {
		if st, ok := orig.Sys().(*syscall.Stat_t); ok {
			// Best effort: only root can give a file away; a non-root
			// operator editing their own config keeps their ownership anyway.
			_ = tmp.Chown(int(st.Uid), int(st.Gid))
		}
	})
}

// writeFileAtomic creates or replaces path with data so readers never see a
// partially written file.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	return stageAndRename(path, data, mode, nil)
}

func stageAndRename(path string, data []byte, mode fs.FileMode, prepare func(*os.File)) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+wizardStagingFilePattern)
	if err != nil {
		return fmt.Errorf("create staging file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	// CreateTemp uses 0600; set the intended mode before any data lands.
	if err := tmp.Chmod(mode); err != nil {
		return fail(fmt.Errorf("chmod staging file %s: %w", tmpName, err))
	}
	if prepare != nil {
		prepare(tmp)
	}
	if err := writeAndSync(tmp, data); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write staging file %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}

// writeAndSync writes data, flushes it to disk and closes f.
func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
