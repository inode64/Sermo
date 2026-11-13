package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"sermo/internal/execx"
	"sermo/internal/hostfs"
)

// UnitPresence distinguishes a missing optional companion from a failed query.
type UnitPresence interface {
	UnitExists(ctx context.Context, unit string) (bool, error)
}

func (m systemdManager) UnitExists(ctx context.Context, unit string) (bool, error) {
	result, err := runSystemctlShow(ctx, m.runner, defaultDetectTimeout, systemctlPropertyLoadState, systemdUnit(unit))
	if err != nil || result.ExitCode != execx.ExitCodeSuccess {
		return false, fmt.Errorf("query unit presence: %s", execx.OperatorFailure(err, result, defaultDetectTimeout))
	}
	state := strings.TrimSpace(result.Stdout)
	if state == "" {
		return false, errors.New("empty unit load state")
	}
	return state != systemdLoadStateNotFound, nil
}

func (m openrcManager) UnitExists(ctx context.Context, unit string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("query OpenRC unit: %w", err)
	}
	path, ok := openRCUnitPath(openRCInitDir, unit)
	if !ok {
		return false, errors.New("invalid OpenRC unit name")
	}
	read := m.readFile
	if read == nil {
		read = hostfs.ReadFile
	}
	_, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
