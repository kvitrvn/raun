package sqlite

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

// Accounts is the store of the identity context on the same database file:
// accounts, forge identities, their sealed tokens, login states and
// sign-ups. Its keyring seals the tokens; without one, storing or reading
// tokens fails with ErrNoKey.
type Accounts struct {
	s    *Store
	keys *Keyring
}

var _ identity.Store = Accounts{}

// Accounts returns the identity store of this database, sealing secrets
// with keys.
func (s *Store) Accounts(keys *Keyring) Accounts {
	return Accounts{s: s, keys: keys}
}

// sealedColumns are every column that holds a sealed secret, with the column
// that keys its rows: the rotation at start goes through them all.
var sealedColumns = []struct {
	table, column, row string
}{
	{"tokens", "sealed", "forge || ':' || forge_id"},
	{"signups", "sealed", "session"},
}

// storedTokens is the plaintext of sealed tokens, written from the values of
// the secrets: a Secret encodes as [redacted].
type storedTokens struct {
	Access        string    `json:"access"`
	AccessExpiry  time.Time `json:"access_expiry"`
	Refresh       string    `json:"refresh"`
	RefreshExpiry time.Time `json:"refresh_expiry"`
}

func (a Accounts) sealTokens(p place, t identity.Tokens) ([]byte, error) {
	plain, err := json.Marshal(storedTokens{
		Access: t.Access.Reveal(), AccessExpiry: t.AccessExpiry.UTC(),
		Refresh: t.Refresh.Reveal(), RefreshExpiry: t.RefreshExpiry.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode tokens: %w", err)
	}
	return a.keys.seal(p, plain)
}

func (a Accounts) openTokens(p place, sealed []byte) (identity.Tokens, error) {
	plain, _, err := a.keys.open(p, sealed)
	if err != nil {
		return identity.Tokens{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	var st storedTokens
	if err := dec.Decode(&st); err != nil {
		return identity.Tokens{}, fmt.Errorf("%w: %s.%s of %s holds no tokens", errSealed, p.table, p.column, p.row)
	}
	return identity.Tokens{
		Access: identity.Secret(st.Access), AccessExpiry: st.AccessExpiry,
		Refresh: identity.Secret(st.Refresh), RefreshExpiry: st.RefreshExpiry,
	}, nil
}

// splitKey splits the key of an identity, forge:id.
func splitKey(key string) (forge, id string, ok bool) {
	forge, id, ok = strings.Cut(key, ":")
	return forge, id, ok && forge != "" && id != ""
}

func tokensPlace(key string) place { return place{"tokens", "sealed", key} }

func signupPlace(session string) place { return place{"signups", "sealed", session} }

func (a Accounts) PutLoginState(ctx context.Context, st identity.LoginState) error {
	return a.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO login_states (hash, verifier, session, return_path, expires_at) VALUES (?, ?, ?, ?, ?)`,
			st.Hash, st.Verifier, st.Session, st.ReturnPath, formatTime(st.ExpiresAt))
		if err != nil {
			return fmt.Errorf("put login state: %w", err)
		}
		return nil
	})
}

func (a Accounts) TakeLoginState(ctx context.Context, hash, session string, now time.Time) (identity.LoginState, error) {
	var st identity.LoginState
	err := a.s.write(ctx, func(tx *sql.Tx) error {
		var expires string
		err := tx.QueryRowContext(ctx,
			`SELECT hash, verifier, session, return_path, expires_at FROM login_states WHERE hash = ?`, hash).
			Scan(&st.Hash, &st.Verifier, &st.Session, &st.ReturnPath, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("take login state: %w", err)
		}
		// Taken whatever follows: a state never serves twice.
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_states WHERE hash = ?`, hash); err != nil {
			return fmt.Errorf("take login state: %w", err)
		}
		if st.ExpiresAt, err = parseTime(expires); err != nil {
			return fmt.Errorf("take login state: expires_at: %w", err)
		}
		return nil
	})
	if errors.Is(err, identity.ErrNotFound) {
		return identity.LoginState{}, err
	}
	if err != nil {
		return identity.LoginState{}, err
	}
	if subtle.ConstantTimeCompare([]byte(st.Session), []byte(session)) != 1 || !now.Before(st.ExpiresAt) {
		return identity.LoginState{}, identity.ErrNotFound
	}
	return st, nil
}

func (a Accounts) DeleteExpiredLoginStates(ctx context.Context, now time.Time) (int, error) {
	return a.deleteExpired(ctx, "login_states", now)
}

func (a Accounts) DeleteExpiredSignups(ctx context.Context, now time.Time) (int, error) {
	return a.deleteExpired(ctx, "signups", now)
}

// deleteExpired deletes the rows of table, login_states or signups, expired
// at now.
func (a Accounts) deleteExpired(ctx context.Context, table string, now time.Time) (int, error) {
	var n int64
	err := a.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE expires_at <= ?`, formatTime(now))
		if err != nil {
			return fmt.Errorf("delete expired %s: %w", table, err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

func (a Accounts) PutSignup(ctx context.Context, sg identity.Signup) error {
	sealed, err := a.sealTokens(signupPlace(sg.Session), sg.Tokens)
	if err != nil {
		return fmt.Errorf("put sign-up: %w", err)
	}
	return a.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO signups (session, forge, forge_id, login, name, proposed, sealed, return_path, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (session) DO UPDATE SET forge = excluded.forge, forge_id = excluded.forge_id,
			   login = excluded.login, name = excluded.name, proposed = excluded.proposed,
			   sealed = excluded.sealed, return_path = excluded.return_path, expires_at = excluded.expires_at`,
			sg.Session, sg.Identity.Forge, sg.Identity.ID, sg.Identity.Login, sg.Identity.Name,
			sg.Proposed, sealed, sg.ReturnPath, formatTime(sg.ExpiresAt))
		if err != nil {
			return fmt.Errorf("put sign-up: %w", err)
		}
		return nil
	})
}

// readSignup reads the sign-up of session inside tx, if it has not expired at
// now.
func (a Accounts) readSignup(ctx context.Context, tx *sql.Tx, session string, now time.Time) (identity.Signup, error) {
	sg := identity.Signup{Session: session}
	var sealed []byte
	var expires string
	err := tx.QueryRowContext(ctx,
		`SELECT forge, forge_id, login, name, proposed, sealed, return_path, expires_at
		 FROM signups WHERE session = ? AND expires_at > ?`, session, formatTime(now)).
		Scan(&sg.Identity.Forge, &sg.Identity.ID, &sg.Identity.Login, &sg.Identity.Name,
			&sg.Proposed, &sealed, &sg.ReturnPath, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Signup{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.Signup{}, fmt.Errorf("read sign-up: %w", err)
	}
	if sg.ExpiresAt, err = parseTime(expires); err != nil {
		return identity.Signup{}, fmt.Errorf("read sign-up: expires_at: %w", err)
	}
	if sg.Tokens, err = a.openTokens(signupPlace(session), sealed); err != nil {
		return identity.Signup{}, fmt.Errorf("read sign-up: %w", err)
	}
	return sg, nil
}

func (a Accounts) Signup(ctx context.Context, session string, now time.Time) (identity.Signup, error) {
	var sg identity.Signup
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		sg, err = a.readSignup(ctx, tx, session, now)
		return err
	})
	return sg, err
}

func (a Accounts) CompleteSignup(ctx context.Context, session string, account identity.Account, now time.Time) (identity.Signup, error) {
	var sg identity.Signup
	err := a.s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if sg, err = a.readSignup(ctx, tx, session, now); err != nil {
			return err
		}
		// The write transaction holds the write lock: what is checked free
		// here stays free until the inserts, which the unique constraints
		// back.
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE name = ?`, account.Name).Scan(&taken); err != nil {
			return fmt.Errorf("complete sign-up: %w", err)
		}
		if taken > 0 {
			return identity.ErrNameTaken
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM identities WHERE forge = ? AND forge_id = ?`,
			sg.Identity.Forge, sg.Identity.ID).Scan(&taken); err != nil {
			return fmt.Errorf("complete sign-up: %w", err)
		}
		if taken > 0 {
			return identity.ErrIdentityTaken
		}
		sealed, err := a.sealTokens(tokensPlace(sg.Identity.Key()), sg.Tokens)
		if err != nil {
			return fmt.Errorf("complete sign-up: %w", err)
		}
		for _, stmt := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO accounts (id, name, created_at) VALUES (?, ?, ?)`, []any{account.ID, account.Name, formatTime(account.CreatedAt)}},
			{`INSERT INTO identities (forge, forge_id, account, login, name) VALUES (?, ?, ?, ?, ?)`,
				[]any{sg.Identity.Forge, sg.Identity.ID, account.ID, sg.Identity.Login, sg.Identity.Name}},
			{`INSERT INTO tokens (forge, forge_id, sealed) VALUES (?, ?, ?)`, []any{sg.Identity.Forge, sg.Identity.ID, sealed}},
			{`DELETE FROM signups WHERE session = ?`, []any{session}},
		} {
			if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
				return fmt.Errorf("complete sign-up: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return identity.Signup{}, err
	}
	return sg, nil
}

func scanAccount(row *sql.Row) (identity.Account, error) {
	var acc identity.Account
	var created string
	err := row.Scan(&acc.ID, &acc.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Account{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.Account{}, fmt.Errorf("read account: %w", err)
	}
	if acc.CreatedAt, err = parseTime(created); err != nil {
		return identity.Account{}, fmt.Errorf("read account: created_at: %w", err)
	}
	return acc, nil
}

func (a Accounts) AccountOf(ctx context.Context, key string) (identity.Account, error) {
	forge, id, ok := splitKey(key)
	if !ok {
		return identity.Account{}, identity.ErrNotFound
	}
	var acc identity.Account
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		acc, err = scanAccount(tx.QueryRowContext(ctx,
			`SELECT a.id, a.name, a.created_at FROM identities i JOIN accounts a ON a.id = i.account
			 WHERE i.forge = ? AND i.forge_id = ?`, forge, id))
		return err
	})
	return acc, err
}

func (a Accounts) Account(ctx context.Context, id string) (identity.Account, error) {
	var acc identity.Account
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		var err error
		acc, err = scanAccount(tx.QueryRowContext(ctx, `SELECT id, name, created_at FROM accounts WHERE id = ?`, id))
		return err
	})
	return acc, err
}

func (a Accounts) UpdateIdentity(ctx context.Context, id identity.Identity) error {
	return a.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE identities SET login = ?, name = ? WHERE forge = ? AND forge_id = ?`,
			id.Login, id.Name, id.Forge, id.ID)
		if err != nil {
			return fmt.Errorf("update identity: %w", err)
		}
		return oneRow(res)
	})
}

// oneRow is identity.ErrNotFound when res changed no row.
func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return identity.ErrNotFound
	}
	return nil
}

func (a Accounts) Tokens(ctx context.Context, key string) (identity.Tokens, error) {
	forge, id, ok := splitKey(key)
	if !ok {
		return identity.Tokens{}, identity.ErrNotFound
	}
	var sealed []byte
	err := a.s.read(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT sealed FROM tokens WHERE forge = ? AND forge_id = ?`, forge, id).Scan(&sealed)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read tokens: %w", err)
		}
		return nil
	})
	if err != nil {
		return identity.Tokens{}, err
	}
	t, err := a.openTokens(tokensPlace(key), sealed)
	if err != nil {
		return identity.Tokens{}, fmt.Errorf("read tokens: %w", err)
	}
	return t, nil
}

func (a Accounts) PutTokens(ctx context.Context, key string, t identity.Tokens) error {
	forge, id, ok := splitKey(key)
	if !ok {
		return identity.ErrNotFound
	}
	sealed, err := a.sealTokens(tokensPlace(key), t)
	if err != nil {
		return fmt.Errorf("put tokens: %w", err)
	}
	return a.s.write(ctx, func(tx *sql.Tx) error {
		var known int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM identities WHERE forge = ? AND forge_id = ?`, forge, id).Scan(&known); err != nil {
			return fmt.Errorf("put tokens: %w", err)
		}
		if known == 0 {
			return identity.ErrNotFound
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO tokens (forge, forge_id, sealed) VALUES (?, ?, ?)
			 ON CONFLICT (forge, forge_id) DO UPDATE SET sealed = excluded.sealed`, forge, id, sealed)
		if err != nil {
			return fmt.Errorf("put tokens: %w", err)
		}
		return nil
	})
}

func (a Accounts) RevokeTokens(ctx context.Context, key string) error {
	forge, id, ok := splitKey(key)
	if !ok {
		return nil
	}
	return a.s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE forge = ? AND forge_id = ?`, forge, id); err != nil {
			return fmt.Errorf("revoke tokens: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE account <> '' AND account = (SELECT account FROM identities WHERE forge = ? AND forge_id = ?)`,
			forge, id)
		if err != nil {
			return fmt.Errorf("revoke tokens: end the sessions: %w", err)
		}
		return nil
	})
}

// RotateSecrets seals again under RAUN_SECRET_KEY every stored secret sealed
// under RAUN_SECRET_KEY_PREVIOUS, in one transaction, and returns how many.
// The server runs it at start, after the migrations: a secret under an
// unknown key, or any secret without a keyring, stops it with nothing
// changed.
func (a Accounts) RotateSecrets(ctx context.Context) (int, error) {
	n := 0
	err := a.s.write(ctx, func(tx *sql.Tx) error {
		for _, c := range sealedColumns {
			type row struct {
				key    string
				sealed []byte
			}
			var rows []row
			rs, err := tx.QueryContext(ctx, `SELECT `+c.row+`, `+c.column+` FROM `+c.table)
			if err != nil {
				return fmt.Errorf("rotate secrets: %w", err)
			}
			for rs.Next() {
				var r row
				if err := rs.Scan(&r.key, &r.sealed); err != nil {
					_ = rs.Close()
					return fmt.Errorf("rotate secrets: %w", err)
				}
				rows = append(rows, r)
			}
			if err := rs.Close(); err != nil {
				return fmt.Errorf("rotate secrets: %w", err)
			}
			if err := rs.Err(); err != nil {
				return fmt.Errorf("rotate secrets: %w", err)
			}
			for _, r := range rows {
				p := place{c.table, c.column, r.key}
				plain, previous, err := a.keys.open(p, r.sealed)
				if err != nil {
					return fmt.Errorf("rotate secrets: %w", err)
				}
				if !previous {
					continue
				}
				sealed, err := a.keys.seal(p, plain)
				if err != nil {
					return fmt.Errorf("rotate secrets: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE `+c.table+` SET `+c.column+` = ? WHERE `+c.row+` = ?`, sealed, r.key); err != nil {
					return fmt.Errorf("rotate secrets: %w", err)
				}
				n++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
