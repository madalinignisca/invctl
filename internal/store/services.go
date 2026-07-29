package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
)

// ---------- applications ----------

// CreateApplication inserts an application.
func (s *SQLStore) CreateApplication(ctx context.Context, actor domain.Actor, app *domain.Application) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`INSERT INTO application (id, code, name, owner_team, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			app.ID, app.Code, app.Name, app.OwnerTeam, app.CreatedAt, app.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating application")
		}
		if err := t.logCreate(ctx, "application", app.ID, app); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "application", EntityID: app.ID,
			Title: app.Name, Subtitle: app.Code,
		})
	})
}

// ListApplications returns every application.
func (s *SQLStore) ListApplications(ctx context.Context) ([]domain.Application, error) {
	var apps []domain.Application
	if err := s.read(ctx, &apps, `SELECT * FROM application ORDER BY name`); err != nil {
		return nil, fmt.Errorf("listing applications: %w", err)
	}
	return apps, nil
}

// ---------- services ----------

// ServiceFilter narrows a service list query.
type ServiceFilter struct {
	EnvironmentID  string
	Kind           string
	ApplicationID  string
	Availability   string
	Tier           int
	Query          string
	IncludeRetired bool
}

// ServiceRow is a service plus what a list view needs to render without
// issuing a query per row.
type ServiceRow struct {
	domain.Service
	EnvironmentCode string `db:"environment_code"`
	EnvironmentRole string `db:"environment_role"`
	ApplicationName string `db:"application_name"`
	InstanceCount   int    `db:"instance_count"`
}

const serviceSelect = `
	SELECT s.*,
	       e.code AS environment_code,
	       e.role AS environment_role,
	       COALESCE(app.name, '') AS application_name,
	       (SELECT COUNT(*) FROM service_instance si WHERE si.service_id = s.id) AS instance_count
	FROM service s
	JOIN environment e ON e.id = s.environment_id
	LEFT JOIN application app ON app.id = s.application_id`

// ListServices returns services matching the filter.
func (s *SQLStore) ListServices(ctx context.Context, f ServiceFilter) ([]ServiceRow, error) {
	var where []string
	var args []any

	if f.EnvironmentID != "" {
		where = append(where, `s.environment_id = ?`)
		args = append(args, f.EnvironmentID)
	}
	if f.Kind != "" {
		where = append(where, `s.kind = ?`)
		args = append(args, f.Kind)
	}
	if f.ApplicationID != "" {
		where = append(where, `s.application_id = ?`)
		args = append(args, f.ApplicationID)
	}
	if f.Availability != "" {
		where = append(where, `s.availability = ?`)
		args = append(args, f.Availability)
	}
	if f.Tier > 0 {
		where = append(where, `s.tier = ?`)
		args = append(args, f.Tier)
	}
	if f.Query != "" {
		where = append(where, `(LOWER(s.name) LIKE ? OR LOWER(s.code) LIKE ?)`)
		pattern := "%" + lower(f.Query) + "%"
		args = append(args, pattern, pattern)
	}
	if !f.IncludeRetired {
		where = append(where, `s.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}

	var rows []ServiceRow
	query := serviceSelect + whereClause(where) + ` ORDER BY s.tier ASC, s.code ASC`
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	return rows, nil
}

// GetService loads one service with its environment and application resolved.
func (s *SQLStore) GetService(ctx context.Context, id string) (*ServiceRow, error) {
	var row ServiceRow
	if err := s.readOne(ctx, &row, serviceSelect+` WHERE s.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting service %s: %w", id, err)
	}
	return &row, nil
}

// GetServiceByCode loads one service by its unique code.
func (s *SQLStore) GetServiceByCode(ctx context.Context, code string) (*ServiceRow, error) {
	var row ServiceRow
	if err := s.readOne(ctx, &row, serviceSelect+` WHERE s.code = ?`, code); err != nil {
		return nil, fmt.Errorf("getting service %s: %w", code, err)
	}
	return &row, nil
}

// CreateService inserts a service.
func (s *SQLStore) CreateService(ctx context.Context, actor domain.Actor, svc *domain.Service) error {
	if err := svc.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO service (id, application_id, code, name, kind, environment_id, availability,
			                     min_healthy, failover_mode, tier, rto_minutes, rpo_minutes,
			                     owner_team, lifecycle, attrs, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			svc.ID, svc.ApplicationID, svc.Code, svc.Name, svc.Kind, svc.EnvironmentID, svc.Availability,
			svc.MinHealthy, svc.FailoverMode, svc.Tier, svc.RTOMinutes, svc.RPOMinutes,
			svc.OwnerTeam, svc.Lifecycle, svc.Attrs, svc.CreatedAt, svc.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating service")
		}
		if err := t.logCreate(ctx, "service", svc.ID, svc); err != nil {
			return err
		}
		return s.indexService(ctx, t, svc)
	})
}

// UpdateService persists field changes to a service.
func (s *SQLStore) UpdateService(ctx context.Context, actor domain.Actor, svc *domain.Service) error {
	if err := svc.Validate(); err != nil {
		return err
	}
	before, err := s.GetService(ctx, svc.ID)
	if err != nil {
		return err
	}
	svc.CreatedAt = before.CreatedAt
	svc.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			UPDATE service SET application_id = ?, code = ?, name = ?, kind = ?, environment_id = ?,
			                   availability = ?, min_healthy = ?, failover_mode = ?, tier = ?,
			                   rto_minutes = ?, rpo_minutes = ?, owner_team = ?, lifecycle = ?,
			                   attrs = ?, updated_at = ?
			WHERE id = ?`,
			svc.ApplicationID, svc.Code, svc.Name, svc.Kind, svc.EnvironmentID,
			svc.Availability, svc.MinHealthy, svc.FailoverMode, svc.Tier,
			svc.RTOMinutes, svc.RPOMinutes, svc.OwnerTeam, svc.Lifecycle,
			svc.Attrs, svc.UpdatedAt, svc.ID)
		if err != nil {
			return translateWriteErr(err, "updating service")
		}
		if err := t.logUpdate(ctx, "service", svc.ID, &before.Service, svc); err != nil {
			return err
		}
		return s.indexService(ctx, t, svc)
	})
}

// RetireService soft-deletes a service.
func (s *SQLStore) RetireService(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetService(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx, `UPDATE service SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring service")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "service", id, domain.ActionRetire, diff)
	})
}

func (s *SQLStore) indexService(ctx context.Context, t *tx, svc *domain.Service) error {
	body := svc.Kind + " " + svc.Availability
	if svc.OwnerTeam != nil {
		body += " " + *svc.OwnerTeam
	}
	return s.indexEntity(ctx, t, searchDoc{
		EntityType: "service", EntityID: svc.ID,
		Title: svc.Name, Subtitle: svc.Code, Body: body,
	})
}

// ---------- service instances ----------

// InstanceRow is an instance plus its host and service, which every view of an
// instance needs.
type InstanceRow struct {
	domain.ServiceInstance
	HostName    string `db:"host_name"`
	HostKind    string `db:"host_kind"`
	ServiceCode string `db:"service_code"`
	ServiceName string `db:"service_name"`
}

const instanceSelect = `
	SELECT si.*, a.name AS host_name, a.kind AS host_kind,
	       s.code AS service_code, s.name AS service_name
	FROM service_instance si
	JOIN asset a ON a.id = si.host_asset_id
	JOIN service s ON s.id = si.service_id`

// ListInstancesByService returns every placement of a service.
func (s *SQLStore) ListInstancesByService(ctx context.Context, serviceID string) ([]InstanceRow, error) {
	var rows []InstanceRow
	err := s.read(ctx, &rows, instanceSelect+` WHERE si.service_id = ? ORDER BY si.ordinal, a.name`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("listing instances of service %s: %w", serviceID, err)
	}
	return rows, nil
}

// ListInstancesByHost returns everything running on a host, used by the asset
// detail page.
func (s *SQLStore) ListInstancesByHost(ctx context.Context, assetID string) ([]InstanceRow, error) {
	var rows []InstanceRow
	err := s.read(ctx, &rows, instanceSelect+` WHERE si.host_asset_id = ? ORDER BY s.code, si.ordinal`, assetID)
	if err != nil {
		return nil, fmt.Errorf("listing instances on host %s: %w", assetID, err)
	}
	return rows, nil
}

// GetInstance loads one instance.
func (s *SQLStore) GetInstance(ctx context.Context, id string) (*InstanceRow, error) {
	var row InstanceRow
	if err := s.readOne(ctx, &row, instanceSelect+` WHERE si.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting instance %s: %w", id, err)
	}
	return &row, nil
}

// CreateInstance places a service on a host.
func (s *SQLStore) CreateInstance(ctx context.Context, actor domain.Actor, si *domain.ServiceInstance) error {
	if err := si.Validate(); err != nil {
		return err
	}
	// Rule 7. A fabricated workload inside an in_scope environment must not be
	// able to render to an operator as hand-asserted fact; the 00008 CHECK
	// constrains the value, this constrains the writer.
	if err := domain.CheckProvenanceWrite(actor, si.Source); err != nil {
		return err
	}
	// A workload on a patch panel is a data-entry mistake, and the impact
	// engine's placement phase would silently inherit it.
	host, err := s.GetAsset(ctx, si.HostAssetID)
	if err != nil {
		return err
	}
	if !host.CanHostInstances() {
		ve := &domain.ValidationError{}
		ve.Add("host_asset_id", "a %s cannot host a service instance", host.Kind)
		return ve
	}

	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO service_instance (id, service_id, host_asset_id, runtime_type, role, shard,
			                              ordinal, desired_state, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			si.ID, si.ServiceID, si.HostAssetID, si.RuntimeType, si.Role, si.Shard,
			si.Ordinal, si.DesiredState, si.Source, si.CreatedAt, si.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating service instance")
		}
		return t.logCreate(ctx, "service_instance", si.ID, si)
	})
}

// UpdateInstance persists field changes to a placement.
//
// It writes declared columns only. Until migration 00008 this statement also
// carried observed_state and observed_at from the round-tripped struct, so a
// stale read silently reverted a concurrent operator edit and logUpdate
// attributed the revert to the human. The columns are gone rather than merely
// omitted here: a boundary that can only be described as "must not write X" is
// not implemented (docs/AUDIT.md rule 1).
func (s *SQLStore) UpdateInstance(ctx context.Context, actor domain.Actor, si *domain.ServiceInstance) error {
	if err := si.Validate(); err != nil {
		return err
	}
	if err := domain.CheckProvenanceWrite(actor, si.Source); err != nil {
		return err
	}
	before, err := s.GetInstance(ctx, si.ID)
	if err != nil {
		return err
	}
	si.CreatedAt = before.CreatedAt
	si.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			UPDATE service_instance
			SET host_asset_id = ?, runtime_type = ?, role = ?, shard = ?, ordinal = ?,
			    desired_state = ?, source = ?, updated_at = ?
			WHERE id = ?`,
			si.HostAssetID, si.RuntimeType, si.Role, si.Shard, si.Ordinal,
			si.DesiredState, si.Source, si.UpdatedAt, si.ID)
		if err != nil {
			return translateWriteErr(err, "updating service instance")
		}
		return t.logUpdate(ctx, "service_instance", si.ID, &before.ServiceInstance, si)
	})
}

// RetireInstance withdraws a placement from the estate.
//
// It writes lifecycle, not desired_state. Those were one column until 00002,
// and this method is why: it always logged domain.ActionRetire, so the intent
// was "this placement is gone" while the write said "it is deliberately not
// running". Two different facts, and the audit entry recorded the one the
// column could not.
//
// desired_state is left alone. What somebody last asked this placement to do is
// part of the record of why it was withdrawn, and overwriting it would destroy
// that for no gain -- the partial unique index keys on lifecycle, so a retired
// row no longer reserves its slot whatever its intent says.
func (s *SQLStore) RetireInstance(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE service_instance SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring service instance")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "service_instance", id, domain.ActionRetire, diff)
	})
}

// ---------- runtime detail ----------

// SetRuntimeSystemd upserts the systemd detail for an instance.
func (s *SQLStore) SetRuntimeSystemd(ctx context.Context, actor domain.Actor, rt *domain.RTSystemd) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO rt_systemd (instance_id, unit_name, unit_type, exec_start, run_as_user,
			                        run_as_group, restart, unit_after, unit_requires, drop_ins)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (instance_id) DO UPDATE SET
			  unit_name = EXCLUDED.unit_name, unit_type = EXCLUDED.unit_type,
			  exec_start = EXCLUDED.exec_start, run_as_user = EXCLUDED.run_as_user,
			  run_as_group = EXCLUDED.run_as_group, restart = EXCLUDED.restart,
			  unit_after = EXCLUDED.unit_after, unit_requires = EXCLUDED.unit_requires,
			  drop_ins = EXCLUDED.drop_ins`,
			rt.InstanceID, rt.UnitName, rt.UnitType, rt.ExecStart, rt.RunAsUser,
			rt.RunAsGroup, rt.Restart, rt.UnitAfter, rt.UnitRequires, rt.DropIns)
		if err != nil {
			return translateWriteErr(err, "setting systemd runtime")
		}
		return t.logCreate(ctx, "rt_systemd", rt.InstanceID, rt)
	})
}

// SetRuntimeContainer upserts the container detail for an instance.
func (s *SQLStore) SetRuntimeContainer(ctx context.Context, actor domain.Actor, rt *domain.RTContainer) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO rt_container (instance_id, engine, container_name, compose_project,
			                          compose_service, image_repo, image_tag, image_digest,
			                          restart_policy, network_mode, rootless)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (instance_id) DO UPDATE SET
			  engine = EXCLUDED.engine, container_name = EXCLUDED.container_name,
			  compose_project = EXCLUDED.compose_project, compose_service = EXCLUDED.compose_service,
			  image_repo = EXCLUDED.image_repo, image_tag = EXCLUDED.image_tag,
			  image_digest = EXCLUDED.image_digest, restart_policy = EXCLUDED.restart_policy,
			  network_mode = EXCLUDED.network_mode, rootless = EXCLUDED.rootless`,
			rt.InstanceID, rt.Engine, rt.ContainerName, rt.ComposeProject,
			rt.ComposeService, rt.ImageRepo, rt.ImageTag, rt.ImageDigest,
			rt.RestartPolicy, rt.NetworkMode, rt.Rootless)
		if err != nil {
			return translateWriteErr(err, "setting container runtime")
		}
		return t.logCreate(ctx, "rt_container", rt.InstanceID, rt)
	})
}

// SetRuntimeWindows upserts the Windows service detail for an instance.
func (s *SQLStore) SetRuntimeWindows(ctx context.Context, actor domain.Actor, rt *domain.RTWindows) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO rt_windows (instance_id, service_name, display_name, binary_path,
			                        start_type, logon_identity_id, depends_on_svc, recovery_action)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (instance_id) DO UPDATE SET
			  service_name = EXCLUDED.service_name, display_name = EXCLUDED.display_name,
			  binary_path = EXCLUDED.binary_path, start_type = EXCLUDED.start_type,
			  logon_identity_id = EXCLUDED.logon_identity_id,
			  depends_on_svc = EXCLUDED.depends_on_svc, recovery_action = EXCLUDED.recovery_action`,
			rt.InstanceID, rt.ServiceName, rt.DisplayName, rt.BinaryPath,
			rt.StartType, rt.LogonIdentityID, rt.DependsOnSvc, rt.RecoveryAction)
		if err != nil {
			return translateWriteErr(err, "setting windows runtime")
		}
		return t.logCreate(ctx, "rt_windows", rt.InstanceID, rt)
	})
}

// SetRuntimeK8s upserts the Kubernetes workload detail for an instance.
func (s *SQLStore) SetRuntimeK8s(ctx context.Context, actor domain.Actor, rt *domain.RTK8s) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO rt_k8s (instance_id, cluster_asset_id, namespace, workload_kind,
			                    workload_name, replicas_desired, service_account, image_digest)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (instance_id) DO UPDATE SET
			  cluster_asset_id = EXCLUDED.cluster_asset_id, namespace = EXCLUDED.namespace,
			  workload_kind = EXCLUDED.workload_kind, workload_name = EXCLUDED.workload_name,
			  replicas_desired = EXCLUDED.replicas_desired,
			  service_account = EXCLUDED.service_account, image_digest = EXCLUDED.image_digest`,
			rt.InstanceID, rt.ClusterAssetID, rt.Namespace, rt.WorkloadKind,
			rt.WorkloadName, rt.ReplicasDesired, rt.ServiceAccount, rt.ImageDigest)
		if err != nil {
			return translateWriteErr(err, "setting k8s runtime")
		}
		return t.logCreate(ctx, "rt_k8s", rt.InstanceID, rt)
	})
}

// RuntimeDetail is whichever runtime table applies to an instance. Exactly one
// field is non-nil.
type RuntimeDetail struct {
	Systemd   *domain.RTSystemd
	Windows   *domain.RTWindows
	Container *domain.RTContainer
	K8s       *domain.RTK8s
}

// GetRuntimeDetail loads the runtime row matching the instance's runtime_type.
func (s *SQLStore) GetRuntimeDetail(ctx context.Context, instanceID, runtimeType string) (*RuntimeDetail, error) {
	detail := &RuntimeDetail{}
	var err error
	switch runtimeType {
	case domain.RuntimeSystemd:
		var rt domain.RTSystemd
		err = s.readOne(ctx, &rt, `SELECT * FROM rt_systemd WHERE instance_id = ?`, instanceID)
		if err == nil {
			detail.Systemd = &rt
		}
	case domain.RuntimeWindowsService:
		var rt domain.RTWindows
		err = s.readOne(ctx, &rt, `SELECT * FROM rt_windows WHERE instance_id = ?`, instanceID)
		if err == nil {
			detail.Windows = &rt
		}
	case domain.RuntimeContainer:
		var rt domain.RTContainer
		err = s.readOne(ctx, &rt, `SELECT * FROM rt_container WHERE instance_id = ?`, instanceID)
		if err == nil {
			detail.Container = &rt
		}
	case domain.RuntimeK8sWorkload:
		var rt domain.RTK8s
		err = s.readOne(ctx, &rt, `SELECT * FROM rt_k8s WHERE instance_id = ?`, instanceID)
		if err == nil {
			detail.K8s = &rt
		}
	default:
		return detail, nil // appliance has no detail table
	}
	// A missing detail row is normal: the operator has not filled it in yet.
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("loading runtime detail for %s: %w", instanceID, err)
	}
	return detail, nil
}
