package store

import "github.com/jackc/pgx/v5/pgtype"

func pgInt64Array(values []int64) pgtype.FlatArray[int64] {
	return pgtype.FlatArray[int64](values)
}
