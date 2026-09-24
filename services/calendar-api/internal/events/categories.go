package events

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/store"
)

// Category groups events and controls their colour and holiday flag.
type Category struct {
	ID         string    `json:"id"`
	Key        string    `json:"key"`
	Name       Text      `json:"name"`
	ColorLight string    `json:"colorLight"`
	ColorDark  string    `json:"colorDark"`
	IsHoliday  bool      `json:"isHoliday"`
	SortOrder  int       `json:"sortOrder"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// CategoryInput creates a category, or is the merged result of a PATCH.
type CategoryInput struct {
	Key        string `json:"key"`
	Name       Text   `json:"name"`
	ColorLight string `json:"colorLight"`
	ColorDark  string `json:"colorDark"`
	IsHoliday  bool   `json:"isHoliday"`
	SortOrder  int    `json:"sortOrder"`
}

// PublicCategory is the category as clients see it.
type PublicCategory struct {
	Key       string `json:"key"`
	Name      Text   `json:"name"`
	Color     Colors `json:"color"`
	IsHoliday bool   `json:"isHoliday"`
	SortOrder int    `json:"sortOrder"`
}

func (c Category) public() PublicCategory {
	return PublicCategory{Key: c.Key, Name: c.Name, Color: Colors{Light: c.ColorLight, Dark: c.ColorDark}, IsHoliday: c.IsHoliday, SortOrder: c.SortOrder}
}

var (
	categoryKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)
	hexColorRe    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

func validateCategory(in *CategoryInput) []apperr.FieldError {
	var fe []apperr.FieldError
	in.Key = strings.TrimSpace(in.Key)
	in.Name.En = strings.TrimSpace(in.Name.En)
	in.Name.Ne = strings.TrimSpace(in.Name.Ne)
	if !categoryKeyRe.MatchString(in.Key) {
		fe = append(fe, apperr.FieldError{Field: "key", Message: "2-41 characters: lower-case letters, digits, underscore; starts with a letter"})
	}
	if in.Name.En == "" || len([]rune(in.Name.En)) > 80 {
		fe = append(fe, apperr.FieldError{Field: "name.en", Message: "required, at most 80 characters"})
	}
	if len([]rune(in.Name.Ne)) > 80 {
		fe = append(fe, apperr.FieldError{Field: "name.ne", Message: "at most 80 characters"})
	}
	if !hexColorRe.MatchString(in.ColorLight) {
		fe = append(fe, apperr.FieldError{Field: "colorLight", Message: "must be a hex colour like #B91C1C"})
	}
	if !hexColorRe.MatchString(in.ColorDark) {
		fe = append(fe, apperr.FieldError{Field: "colorDark", Message: "must be a hex colour like #F87171"})
	}
	return fe
}

const categoryCols = `id::text, key, name, color_light, color_dark, is_holiday, sort_order, created_at, updated_at`

func scanCategory(r pgx.Row) (Category, error) {
	var c Category
	err := r.Scan(&c.ID, &c.Key, &c.Name, &c.ColorLight, &c.ColorDark, &c.IsHoliday, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// ListCategories returns a tenant's categories in display order.
func (s *Service) ListCategories(ctx context.Context, tenantID string) ([]Category, error) {
	return listCategories(ctx, s.pool, tenantID)
}

func listCategories(ctx context.Context, q store.Querier, tenantID string) ([]Category, error) {
	rows, err := q.Query(ctx, `SELECT `+categoryCols+` FROM categories WHERE tenant_id = $1 ORDER BY sort_order, key`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Category, error) { return scanCategory(r) })
}

// PublicCategories returns categories as clients see them.
func (s *Service) PublicCategories(ctx context.Context, tenantID string) ([]PublicCategory, error) {
	cs, err := s.ListCategories(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]PublicCategory, len(cs))
	for i, c := range cs {
		out[i] = c.public()
	}
	return out, nil
}

// CreateCategory adds a category.
func (s *Service) CreateCategory(ctx context.Context, actor audit.Actor, in CategoryInput) (Category, error) {
	if fe := validateCategory(&in); len(fe) > 0 {
		return Category{}, apperr.Validation(fe...)
	}
	var c Category
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanCategory(tx.QueryRow(ctx, `
			INSERT INTO categories (tenant_id, key, name, color_light, color_dark, is_holiday, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+categoryCols,
			actor.TenantID, in.Key, in.Name, in.ColorLight, in.ColorDark, in.IsHoliday, in.SortOrder))
		if err != nil {
			return err
		}
		return s.afterCategoryChange(ctx, tx, actor, "category.create", c.ID, nil, c)
	})
	if ae := apperr.From(err); ae != nil && ae.Code == apperr.CodeConflict {
		return Category{}, apperr.Conflict(apperr.CodeConflict, "A category with key "+in.Key+" already exists.")
	}
	return c, err
}

// PatchCategory applies a JSON merge patch. The key cannot change because clients filter by it.
func (s *Service) PatchCategory(ctx context.Context, actor audit.Actor, id string, patch []byte) (Category, error) {
	var after Category
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := scanCategory(tx.QueryRow(ctx, `SELECT `+categoryCols+` FROM categories WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, actor.TenantID))
		if store.IsNoRows(err) {
			return apperr.NotFound("category")
		}
		if err != nil {
			return err
		}
		cur := CategoryInput{Key: before.Key, Name: before.Name, ColorLight: before.ColorLight, ColorDark: before.ColorDark, IsHoliday: before.IsHoliday, SortOrder: before.SortOrder}
		var in CategoryInput
		if err := ApplyMergePatch(cur, patch, &in); err != nil {
			return err
		}
		fe := validateCategory(&in)
		if in.Key != before.Key {
			fe = append(fe, apperr.FieldError{Field: "key", Message: "cannot be changed; create a new category instead"})
		}
		if len(fe) > 0 {
			return apperr.Validation(fe...)
		}
		after, err = scanCategory(tx.QueryRow(ctx, `
			UPDATE categories SET name = $2, color_light = $3, color_dark = $4, is_holiday = $5, sort_order = $6, updated_at = now()
			WHERE id = $1 RETURNING `+categoryCols, id, in.Name, in.ColorLight, in.ColorDark, in.IsHoliday, in.SortOrder))
		if err != nil {
			return err
		}
		return s.afterCategoryChange(ctx, tx, actor, "category.update", id, before, after)
	})
	return after, err
}

// DeleteCategory removes an unused category.
func (s *Service) DeleteCategory(ctx context.Context, actor audit.Actor, id string) error {
	return store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := scanCategory(tx.QueryRow(ctx, `SELECT `+categoryCols+` FROM categories WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, actor.TenantID))
		if store.IsNoRows(err) {
			return apperr.NotFound("category")
		}
		if err != nil {
			return err
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE category_id = $1`, id).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return apperr.Conflict(apperr.CodeConflict, "The category is used by events (including deleted ones). Move them to another category first.").With("events", used)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM categories WHERE id = $1`, id); err != nil {
			return err
		}
		return s.afterCategoryChange(ctx, tx, actor, "category.delete", id, before, nil)
	})
}

// Category colours and names appear in every bucket, so any change bumps all buckets.
func (s *Service) afterCategoryChange(ctx context.Context, tx pgx.Tx, actor audit.Actor, action, id string, before, after any) error {
	if _, err := store.BumpVersion(ctx, tx, ScopeCategories(actor.TenantID)); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, actor, action, "category", id, before, after); err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, actor.TenantID, "categories", "categories.changed", map[string]any{"categoryId": id}); err != nil {
		return err
	}
	return outbox.EnqueuePurge(ctx, tx, s.purge, s.baseURL+"/v1/manifest")
}

// EnsureDefaultCategories seeds a tenant's first categories (bootstrap).
func EnsureDefaultCategories(ctx context.Context, q store.Querier, tenantID string) (int, error) {
	defaults := []CategoryInput{
		{Key: "public_holiday", Name: Text{En: "Public holiday", Ne: "सार्वजनिक बिदा"}, ColorLight: "#B91C1C", ColorDark: "#F87171", IsHoliday: true, SortOrder: 10},
		{Key: "festival", Name: Text{En: "Festival", Ne: "चाडपर्व"}, ColorLight: "#B45309", ColorDark: "#FBBF24", SortOrder: 20},
		{Key: "observance", Name: Text{En: "Observance", Ne: "दिवस"}, ColorLight: "#1D4ED8", ColorDark: "#60A5FA", SortOrder: 30},
		{Key: "event", Name: Text{En: "Event", Ne: "कार्यक्रम"}, ColorLight: "#047857", ColorDark: "#34D399", SortOrder: 40},
	}
	n := 0
	for _, c := range defaults {
		tag, err := q.Exec(ctx, `
			INSERT INTO categories (tenant_id, key, name, color_light, color_dark, is_holiday, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (tenant_id, key) DO NOTHING`,
			tenantID, c.Key, c.Name, c.ColorLight, c.ColorDark, c.IsHoliday, c.SortOrder)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
