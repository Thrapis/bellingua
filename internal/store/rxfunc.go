package store

import (
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"

	"github.com/Thrapis/bellingua/internal/rx"
)

// rx(pattern, text) is the SQL side of a regex search: 1 when text matches
// the rx pattern, 0 when it does not or is NULL. Patterns are compiled once
// (rx.Cached); a broken one fails the query.
func init() {
	err := sqlite.RegisterDeterministicScalarFunction("rx", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		pat, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("rx: the pattern must be text")
		}
		var text string
		switch v := args[1].(type) {
		case nil:
			return int64(0), nil
		case string:
			text = v
		case []byte:
			text = string(v)
		default:
			text = fmt.Sprint(v)
		}
		re, err := rx.Cached(pat)
		if err != nil {
			return nil, fmt.Errorf("bad pattern %q: %w", pat, err)
		}
		if re.MatchString(text) {
			return int64(1), nil
		}
		return int64(0), nil
	})
	if err != nil {
		panic(err)
	}
}
