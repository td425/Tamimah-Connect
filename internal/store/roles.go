package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Features are the console areas a role's permissions are expressed over. The
// order here is the order the admin Roles editor renders them in.
var Features = []string{
	"extensions", "trunks", "routing", "ivr", "leads",
	"cdr", "analytics", "transports", "settings", "users",
}

// Actions are the four things a role may be allowed to do to a feature.
var Actions = []string{"view", "create", "edit", "delete"}

// Perm is the four-action permission set for a single feature.
type Perm struct {
	View   bool `json:"view"`
	Create bool `json:"create"`
	Edit   bool `json:"edit"`
	Delete bool `json:"delete"`
}

// Permissions maps a feature name to its permission set.
type Permissions map[string]Perm

// Allowed reports whether this permission set grants action on feature.
func (p Permissions) Allowed(feature, action string) bool {
	set, ok := p[feature]
	if !ok {
		return false
	}
	switch action {
	case "view":
		return set.View
	case "create":
		return set.Create
	case "edit":
		return set.Edit
	case "delete":
		return set.Delete
	}
	return false
}

// Role is a console role and the features its holders may use.
type Role struct {
	Name        string      `json:"name"`
	DisplayName string      `json:"displayName"`
	Permissions Permissions `json:"permissions"`
	RequireTOTP bool        `json:"requireTotp"`
	BuiltIn     bool        `json:"builtIn"`
}

// Roles is the store for console roles and their permission matrices.
type Roles struct {
	pool *pgxpool.Pool
}

// NewRoles returns a Roles store bound to a connection pool.
func NewRoles(pool *pgxpool.Pool) *Roles {
	return &Roles{pool: pool}
}

// List returns all roles ordered by name.
func (s *Roles) List(ctx context.Context) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, display_name, permissions, require_totp, built_in
		  FROM tpbx_roles ORDER BY built_in DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one role by name.
func (s *Roles) Get(ctx context.Context, name string) (Role, error) {
	r, err := scanRole(s.pool.QueryRow(ctx, `
		SELECT name, display_name, permissions, require_totp, built_in
		  FROM tpbx_roles WHERE name=$1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func scanRole(row pgx.Row) (Role, error) {
	var r Role
	var raw []byte
	if err := row.Scan(&r.Name, &r.DisplayName, &raw, &r.RequireTOTP, &r.BuiltIn); err != nil {
		return r, err
	}
	r.Permissions = Permissions{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &r.Permissions); err != nil {
			return r, err
		}
	}
	return r, nil
}

// Can reports whether the named role may perform action on feature. The admin
// role is always allowed; a missing role is never allowed.
func (s *Roles) Can(ctx context.Context, role, feature, action string) bool {
	if role == "admin" {
		return true
	}
	r, err := s.Get(ctx, role)
	if err != nil {
		return false
	}
	return r.Permissions.Allowed(feature, action)
}

// Create inserts a new role. The name "admin" is reserved.
func (s *Roles) Create(ctx context.Context, r Role) error {
	if err := validateRoleName(r.Name); err != nil {
		return err
	}
	if r.Name == "admin" {
		return errors.New("the admin role is reserved")
	}
	raw, err := json.Marshal(cleanPerms(r.Permissions))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tpbx_roles (name, display_name, permissions, require_totp, built_in)
		VALUES ($1,$2,$3,$4,false)`, r.Name, r.DisplayName, raw, r.RequireTOTP)
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return ErrConflict
	}
	return err
}

// Update rewrites a role's display name, permissions and TOTP requirement.
// Built-in roles (admin) cannot be modified.
func (s *Roles) Update(ctx context.Context, r Role) error {
	existing, err := s.Get(ctx, r.Name)
	if err != nil {
		return err
	}
	if existing.BuiltIn {
		return errors.New("the built-in admin role cannot be modified")
	}
	raw, err := json.Marshal(cleanPerms(r.Permissions))
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tpbx_roles SET display_name=$2, permissions=$3, require_totp=$4
		 WHERE name=$1`, r.Name, r.DisplayName, raw, r.RequireTOTP)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a role. Built-in roles cannot be deleted, nor can a role that
// still has users assigned to it.
func (s *Roles) Delete(ctx context.Context, name string) error {
	r, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if r.BuiltIn {
		return errors.New("the built-in admin role cannot be deleted")
	}
	var inUse int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tpbx_users WHERE role=$1`, name).Scan(&inUse); err != nil {
		return err
	}
	if inUse > 0 {
		return fmt.Errorf("role %q is assigned to %d user(s); reassign them first", name, inUse)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_roles WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RequiresTOTP reports whether the named role forces TOTP enrolment.
func (s *Roles) RequiresTOTP(ctx context.Context, role string) bool {
	var req bool
	err := s.pool.QueryRow(ctx, `SELECT require_totp FROM tpbx_roles WHERE name=$1`, role).Scan(&req)
	if err != nil {
		return false
	}
	return req
}

// cleanPerms keeps only known features/actions so a role can never carry a
// permission for a feature the backend doesn't enforce.
func cleanPerms(in Permissions) Permissions {
	out := Permissions{}
	for _, f := range Features {
		if p, ok := in[f]; ok {
			out[f] = p
		}
	}
	return out
}

func validateRoleName(name string) error {
	if len(name) < 2 || len(name) > 32 {
		return errors.New("role name must be 2-32 characters")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("role name %q may only contain lowercase letters, digits, _ and -", name)
		}
	}
	return nil
}
