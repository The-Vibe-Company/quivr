package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PluginStore keeps the plugin registry and the active Pipeline Plan.
type PluginStore struct{ Pool *pgxpool.Pool }

// pluginSeedLock serializes the startup seed of api and worker.
const pluginSeedLock = 642002

// SeedPlugins writes the seed's registrations as given and its roles as the
// first active plan when the registry has no registration. A seed with no
// registration writes nothing.
func (s PluginStore) SeedPlugins(ctx context.Context, seed registry.Seed) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", pluginSeedLock); err != nil {
		return false, err
	}
	var registered bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM plugin_registrations)").Scan(&registered); err != nil || registered || len(seed.Registrations) == 0 {
		return false, err
	}
	for _, r := range seed.Registrations {
		if _, err = tx.Exec(ctx, `INSERT INTO plugin_registrations(id,plugin_id,version,endpoint,manifest_digest,artifact_digest,contributions,roles,state) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9)`,
			r.ID, r.PluginID, r.Version, r.Endpoint, r.ManifestDigest, r.ArtifactDigest, r.Contributions, r.Roles, r.State); err != nil {
			return false, err
		}
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return false, err
	}
	plan := "plan_" + hex.EncodeToString(id[:])
	if _, err = tx.Exec(ctx, "INSERT INTO pipeline_plans(id) VALUES($1)", plan); err != nil {
		return false, err
	}
	for _, a := range seed.Roles {
		if _, err = tx.Exec(ctx, "INSERT INTO pipeline_plan_roles(plan_id,role,registration_id) VALUES($1,$2,$3)", plan, a.Role, a.RegistrationID); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO active_pipeline_plan(plan_id) VALUES($1) ON CONFLICT (singleton) DO UPDATE SET plan_id=EXCLUDED.plan_id, activated_at=now()", plan); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// PluginRegistrations lists every registration, oldest first.
func (s PluginStore) PluginRegistrations(ctx context.Context) ([]registry.Registration, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,plugin_id,version,endpoint,manifest_digest,coalesce(artifact_digest,''),contributions,roles,state,created_at,updated_at FROM plugin_registrations ORDER BY created_at,plugin_id,version,id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.Registration, error) {
		var r registry.Registration
		err := row.Scan(&r.ID, &r.PluginID, &r.Version, &r.Endpoint, &r.ManifestDigest, &r.ArtifactDigest, &r.Contributions, &r.Roles, &r.State, &r.CreatedAt, &r.UpdatedAt)
		return r, err
	})
}

// ActivePlan returns the active Pipeline Plan with its roles sorted, or
// registry.ErrNoPlan.
func (s PluginStore) ActivePlan(ctx context.Context) (registry.Plan, error) {
	var plan registry.Plan
	err := s.Pool.QueryRow(ctx, `SELECT p.id,p.created_at,a.activated_at FROM active_pipeline_plan a JOIN pipeline_plans p ON p.id=a.plan_id`).Scan(&plan.ID, &plan.CreatedAt, &plan.ActivatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan, registry.ErrNoPlan
	}
	if err != nil {
		return plan, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT pr.role,pr.registration_id,r.plugin_id,r.version FROM pipeline_plan_roles pr JOIN plugin_registrations r ON r.id=pr.registration_id WHERE pr.plan_id=$1 ORDER BY pr.role`, plan.ID)
	if err != nil {
		return plan, err
	}
	plan.Roles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (registry.Assignment, error) {
		var a registry.Assignment
		err := row.Scan(&a.Role, &a.RegistrationID, &a.PluginID, &a.Version)
		return a, err
	})
	return plan, err
}
