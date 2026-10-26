package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Error is an API error rendered as JSON with the given status.
type Error struct {
	Status int
	Body   any
}

func (e *Error) Error() string {
	b, _ := json.Marshal(e.Body)
	return fmt.Sprintf("%d %s", e.Status, b)
}

func Detail(status int, msg string) *Error {
	return &Error{Status: status, Body: Obj{"detail": msg}}
}

var (
	ErrNotFound     = Detail(404, "Not found.")
	ErrForbidden    = Detail(403, "You do not have permission to perform this action.")
	ErrUnauthorized = Detail(401, "Authentication credentials were not provided.")
)

// FieldErrors collects validation errors keyed by field name, DRF style.
type FieldErrors map[string][]string

func (fe FieldErrors) Add(field, msg string) { fe[field] = append(fe[field], msg) }

func (fe FieldErrors) Err() error {
	if len(fe) == 0 {
		return nil
	}
	body := Obj{}
	for k, v := range fe {
		body[k] = v
	}
	return &Error{Status: 400, Body: body}
}

func FieldError(field, msg string) error {
	return &Error{Status: 400, Body: Obj{field: []string{msg}}}
}

func BadRequest(msg string) error { return Detail(400, msg) }

var keyRe = regexp.MustCompile(`Key \(([^)]*)\)=`)

// translateDBError converts constraint violations into API validation errors.
func translateDBError(res *Resource, err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505": // unique_violation
			cols := []string{}
			if m := keyRe.FindStringSubmatch(pe.Detail); m != nil {
				for _, c := range strings.Split(m[1], ",") {
					cols = append(cols, strings.TrimSpace(c))
				}
			}
			var labels []string
			field := "__all__"
			for _, col := range cols {
				name := strings.TrimSuffix(col, "_id")
				labels = append(labels, title(name))
				if res != nil && len(cols) == 1 {
					field = name
				}
			}
			verbose := "Object"
			if res != nil {
				verbose = res.Verbose
			}
			return FieldError(field, fmt.Sprintf("%s with this %s already exists.", verbose, strings.Join(labels, " and ")))
		case "23503": // foreign_key_violation
			if m := keyRe.FindStringSubmatch(pe.Detail); m != nil {
				col := strings.TrimSpace(m[1])
				name := strings.TrimSuffix(col, "_id")
				if strings.Contains(pe.Detail, "is still referenced") {
					return Detail(409, "Resource is being used by running jobs or other objects.")
				}
				return FieldError(name, "Invalid pk - object does not exist.")
			}
		case "23502": // not_null_violation
			return FieldError(strings.TrimSuffix(pe.ColumnName, "_id"), "This field may not be null.")
		case "22001":
			return BadRequest("Value too long.")
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func title(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func writeError(w http.ResponseWriter, err error) {
	var ae *Error
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, ae.Body)
		return
	}
	if tr := translateDBError(nil, err); tr != err {
		if errors.As(tr, &ae) {
			writeJSON(w, ae.Status, ae.Body)
			return
		}
	}
	slog.Error("internal error", "err", err)
	writeJSON(w, 500, Obj{"detail": "A server error has occurred."})
}
