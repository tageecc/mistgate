//go:build js

package cloudusage

import (
	"context"
	"errors"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func migrate(context.Context, *store.Store) error {
	return errors.New("cloud usage requires the VPS edition")
}
