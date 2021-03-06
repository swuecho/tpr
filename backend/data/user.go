package data

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v4"
	errors "golang.org/x/xerrors"
)

type DuplicationError struct {
	Field string // Field or fields that caused the rejection
}

func (e DuplicationError) Error() string {
	return fmt.Sprintf("%s is already taken", e.Field)
}

func selectUser(ctx context.Context, db Queryer, name, sql string, arg interface{}) (*User, error) {
	user := User{}

	err := prepareQueryRow(ctx, db, name, sql, arg).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordDigest, &user.PasswordSalt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &user, nil
}

const getUserByNameSQL = `select id, name, email, password_digest, password_salt from users where name=$1`

func SelectUserByName(ctx context.Context, db Queryer, name string) (*User, error) {
	return selectUser(ctx, db, "getUserByName", getUserByNameSQL, name)
}

const getUserByEmailSQL = `select id, name, email, password_digest, password_salt from users where email=$1`

func SelectUserByEmail(ctx context.Context, db Queryer, email string) (*User, error) {
	return selectUser(ctx, db, "getUserByEmail", getUserByEmailSQL, email)
}

const getUserBySessionIDSQL = `select users.id, name, email, password_digest, password_salt
from sessions
  join users on sessions.user_id=users.id
where sessions.id=$1`

func SelectUserBySessionID(ctx context.Context, db Queryer, id []byte) (*User, error) {
	return selectUser(ctx, db, "getUserBySessionID", getUserBySessionIDSQL, id)
}

func CreateUser(ctx context.Context, db Queryer, user *User) (int32, error) {
	err := InsertUser(ctx, db, user)
	if err != nil {
		if strings.Contains(err.Error(), "users_name_unq") {
			return 0, DuplicationError{Field: "name"}
		}
		if strings.Contains(err.Error(), "users_email_key") {
			return 0, DuplicationError{Field: "email"}
		}
		return 0, err
	}

	return user.ID.Int, nil
}
