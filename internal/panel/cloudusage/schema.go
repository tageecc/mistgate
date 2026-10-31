//go:build !js

package cloudusage

import (
	"context"
	"embed"
	"errors"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/pressly/goose/v3"
	"io/fs"
)

// The fork's optional schema has its own version sequence. Upstream migrations and backup snapshots stay unchanged.
//
//go:embed migrations/*.sql
var migrations embed.FS

func migrate(ctx context.Context, st *store.Store) error {
	var version int64
	var exists int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='cloud_usage_schema'`).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		if err := st.R.QueryRowContext(ctx, `SELECT coalesce(max(version_id),0) FROM cloud_usage_schema WHERE is_applied=1`).Scan(&version); err != nil {
			return err
		}
		if version > 2 {
			return errors.New("cloud usage schema is newer than this binary")
		}
	}
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, st.W, files, goose.WithTableName("cloud_usage_schema"))
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}
