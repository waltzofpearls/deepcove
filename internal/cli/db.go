package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/waltzofpearls/deepcove/internal/config"
	"github.com/waltzofpearls/deepcove/internal/db"
)

// cmdDB handles `deepcove db`. Only one thing lives here: putting a database right again when an index
// has stopped seeing rows that are still there.
func cmdDB(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: deepcove db <check|repair>")
	}
	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	database, err := db.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("db: %w (stop deepcove first, it holds the database)", err)
	}
	defer func() { _ = database.Close() }()

	switch args[0] {
	case "check", "repair":
		ready, err := db.SetUp(ctx, database)
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("%w: start deepcove once and it builds one", db.ErrNotSetUp)
		}
		if args[0] == "check" {
			return checkDatabase(ctx, database)
		}
		return repairDatabase(ctx, database)
	default:
		return fmt.Errorf("unknown command %q: deepcove db <check|repair>", args[0])
	}
}

func checkDatabase(ctx context.Context, database *sql.DB) error {
	if err := db.VerifyIndexes(ctx, database); err != nil {
		return err
	}
	fmt.Println("the database looks healthy")
	return nil
}

func repairDatabase(ctx context.Context, database *sql.DB) error {
	rebuilt, err := db.RebuildIndexes(ctx, database)
	if err != nil {
		return err
	}
	for _, name := range rebuilt {
		fmt.Println("rebuilt", name)
	}
	if len(rebuilt) == 0 {
		fmt.Println("no rebuildable index, so there was nothing to put right")
	}

	switch err := db.VerifyIndexes(ctx, database); {
	case err == nil:
		fmt.Println("the database looks healthy")
		return nil
	case errors.Is(err, db.ErrIndexLostRows):
		// What is left is the index behind the UNIQUE constraint, which cannot be dropped.
		return fmt.Errorf("%w\nthe damage is in a constraint, which this cannot rebuild", err)
	default:
		return err
	}
}
