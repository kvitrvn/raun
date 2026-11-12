package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

// The columns of an item, in the order every query reads them.
const itemColumns = `id, type, status, revision, title, created_at, updated_at, document`

// List returns the items of the scope that match the filter, by identifier.
// It reads the rows and their history in one read transaction, with two
// queries whatever the number of items.
func (s *Store) List(ctx context.Context, scope knowledge.Scope, filter knowledge.Filter) ([]knowledge.Item, error) {
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	if err := filter.Validate(); err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	where := `workspace = ? AND repository = ?`
	args := []any{scope.Workspace, scope.Repository}
	if filter.Type != "" {
		where += ` AND type = ?`
		args = append(args, string(filter.Type))
	}
	if filter.Status != "" {
		where += ` AND status = ?`
		args = append(args, string(filter.Status))
	}

	var items []knowledge.Item
	err := s.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+itemColumns+` FROM knowledge_items WHERE `+where+` ORDER BY id`, args...)
		if err != nil {
			return err
		}
		byID := map[string]int{}
		for rows.Next() {
			it, err := scanItem(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			byID[it.ID] = len(items)
			items = append(items, it)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		events, err := tx.QueryContext(ctx, `SELECT item, `+eventColumns+` FROM knowledge_history
			WHERE workspace = ? AND repository = ? AND item IN (SELECT id FROM knowledge_items WHERE `+where+`)
			ORDER BY item, position`, append([]any{scope.Workspace, scope.Repository}, args...)...)
		if err != nil {
			return err
		}
		defer func() { _ = events.Close() }()
		for events.Next() {
			var id string
			e, err := scanEvent(events, &id)
			if err != nil {
				return err
			}
			i, ok := byID[id]
			if !ok {
				return fmt.Errorf("event of %s, which was not listed", id)
			}
			items[i].History = append(items[i].History, e)
		}
		return events.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	return items, nil
}

// Get returns the item, or knowledge.ErrNotFound.
func (s *Store) Get(ctx context.Context, scope knowledge.Scope, id string) (knowledge.Item, error) {
	if err := scope.Validate(); err != nil {
		return knowledge.Item{}, fmt.Errorf("get %s: %w", id, err)
	}
	var it knowledge.Item
	err := s.read(ctx, func(tx *sql.Tx) error {
		var err error
		it, err = load(ctx, tx, scope, id)
		return err
	})
	if err != nil {
		return knowledge.Item{}, fmt.Errorf("get %s: %w", id, err)
	}
	return it, nil
}

// Create stores a new item at revision 1 with its creation event.
func (s *Store) Create(ctx context.Context, scope knowledge.Scope, it knowledge.Item) error {
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	if err := knowledge.CheckCreate(it); err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	doc, err := encodeDocument(it)
	if err != nil {
		return err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		var taken int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM knowledge_items WHERE workspace = ? AND repository = ? AND id = ?`,
			scope.Workspace, scope.Repository, it.ID).Scan(&taken)
		if err != nil {
			return err
		}
		if taken > 0 {
			return knowledge.ErrIDTaken
		}
		if err := checkReferences(ctx, tx, scope, it); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_items (workspace, repository, `+itemColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			scope.Workspace, scope.Repository, it.ID, string(it.Type), string(it.Status), it.Revision, it.Title,
			formatTime(it.CreatedAt), formatTime(it.UpdatedAt), doc)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, scope, it.ID, 0, it.History)
	})
	if err != nil {
		return fmt.Errorf("create %s: %w", it.ID, err)
	}
	return nil
}

// Update runs mutate on the stored item inside the write transaction, then
// stores the result at the next revision with its new events.
func (s *Store) Update(ctx context.Context, scope knowledge.Scope, id string, revision int64, mutate knowledge.Mutation) (knowledge.Item, error) {
	if err := scope.Validate(); err != nil {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, err)
	}
	var next knowledge.Item
	// errMutation keeps the error of mutate apart, so it comes back as it is.
	var errMutation error
	err := s.write(ctx, func(tx *sql.Tx) error {
		stored, err := load(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if stored.Revision != revision {
			return fmt.Errorf("%w: read %d, stored %d", knowledge.ErrStaleRevision, revision, stored.Revision)
		}
		if stored.Revision == math.MaxInt64 {
			return fmt.Errorf("revision %d cannot grow", stored.Revision)
		}
		next, errMutation = mutate(stored.Clone())
		if errMutation != nil {
			return errMutation
		}
		if err := knowledge.CheckUpdate(stored, next); err != nil {
			return err
		}
		if err := checkReferences(ctx, tx, scope, next); err != nil {
			return err
		}
		next = next.Clone()
		next.Revision = stored.Revision + 1
		doc, err := encodeDocument(next)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE knowledge_items
			SET status = ?, revision = ?, title = ?, updated_at = ?, document = ?
			WHERE workspace = ? AND repository = ? AND id = ? AND revision = ?`,
			string(next.Status), next.Revision, next.Title, formatTime(next.UpdatedAt), doc,
			scope.Workspace, scope.Repository, id, stored.Revision)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%w: the row changed during the update (%d rows, %v)", knowledge.ErrStaleRevision, n, err)
		}
		return insertEvents(ctx, tx, scope, id, len(stored.History), next.History[len(stored.History):])
	})
	if errMutation != nil {
		return knowledge.Item{}, errMutation
	}
	if err != nil {
		return knowledge.Item{}, fmt.Errorf("update %s: %w", id, err)
	}
	return next.Clone(), nil
}

// load reads an item and its history in the transaction.
func load(ctx context.Context, tx *sql.Tx, scope knowledge.Scope, id string) (knowledge.Item, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM knowledge_items WHERE workspace = ? AND repository = ? AND id = ?`,
		scope.Workspace, scope.Repository, id)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledge.Item{}, knowledge.ErrNotFound
	}
	if err != nil {
		return knowledge.Item{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT item, `+eventColumns+` FROM knowledge_history
		WHERE workspace = ? AND repository = ? AND item = ? ORDER BY position`,
		scope.Workspace, scope.Repository, id)
	if err != nil {
		return knowledge.Item{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item string
		e, err := scanEvent(rows, &item)
		if err != nil {
			return knowledge.Item{}, err
		}
		it.History = append(it.History, e)
	}
	if err := rows.Err(); err != nil {
		return knowledge.Item{}, err
	}
	return it, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanItem(row scanner) (knowledge.Item, error) {
	var it knowledge.Item
	var typ, status, created, updated, doc string
	if err := row.Scan(&it.ID, &typ, &status, &it.Revision, &it.Title, &created, &updated, &doc); err != nil {
		return knowledge.Item{}, err
	}
	it.Type, it.Status = knowledge.Type(typ), knowledge.Status(status)
	var err error
	if it.CreatedAt, err = parseTime(created); err != nil {
		return knowledge.Item{}, err
	}
	if it.UpdatedAt, err = parseTime(updated); err != nil {
		return knowledge.Item{}, err
	}
	if err := decodeDocument(doc, &it); err != nil {
		return knowledge.Item{}, err
	}
	return it, nil
}

// The columns of an event, after the identifier of its item.
const eventColumns = `at, actor, actor_id, from_status, to_status, reason`

func scanEvent(row scanner, item *string) (knowledge.Event, error) {
	var e knowledge.Event
	var at, actor, from, to string
	if err := row.Scan(item, &at, &actor, &e.By, &from, &to, &e.Reason); err != nil {
		return knowledge.Event{}, err
	}
	t, err := parseTime(at)
	if err != nil {
		return knowledge.Event{}, err
	}
	e.At, e.Actor, e.From, e.To = t, knowledge.ActorKind(actor), knowledge.Status(from), knowledge.Status(to)
	return e, nil
}

// insertEvents appends events to the history of an item, numbered from first.
func insertEvents(ctx context.Context, tx *sql.Tx, scope knowledge.Scope, id string, first int, events []knowledge.Event) error {
	for i, e := range events {
		_, err := tx.ExecContext(ctx, `INSERT INTO knowledge_history (workspace, repository, item, position, `+eventColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			scope.Workspace, scope.Repository, id, first+i,
			formatTime(e.At), string(e.Actor), e.By, string(e.From), string(e.To), e.Reason)
		if err != nil {
			return fmt.Errorf("append event %d: %w", first+i, err)
		}
	}
	return nil
}

// checkReferences refuses an item that names a persona the scope does not
// hold. It runs inside the write transaction, so the personas cannot change
// before the commit.
func checkReferences(ctx context.Context, tx *sql.Tx, scope knowledge.Scope, it knowledge.Item) error {
	refs := it.PersonaReferences()
	if len(refs) == 0 {
		return nil
	}
	args := []any{scope.Workspace, scope.Repository, string(knowledge.TypePersona)}
	for _, r := range refs {
		args = append(args, r)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM knowledge_items
		WHERE workspace = ? AND repository = ? AND type = ? AND id IN (?`+strings.Repeat(", ?", len(refs)-1)+`)`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var missing []string
	for _, r := range refs {
		if !found[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: personas %s", knowledge.ErrUnknownReference, strings.Join(missing, ", "))
	}
	return nil
}
