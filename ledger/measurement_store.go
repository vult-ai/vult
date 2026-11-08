package ledger

import (
	"context"
	"math/big"

	"github.com/jackc/pgx/v5"
)

func emptyContexts(values []ContextMeasurement) any {
	if len(values) == 0 {
		return nil
	}
	return values
}

func prepareMeasurements(ctx context.Context, tx pgx.Tx, s Scope, q *PostJournal) error {
	book, err := readBook(ctx, tx, s)
	if err != nil {
		return err
	}
	accounts := map[string]Account{}
	for i := range q.Lines {
		line := &q.Lines[i]
		if line.Native != nil {
			a, ok := accounts[line.AccountID]
			if !ok {
				a, err = readAccount(ctx, tx, s, line.AccountID)
				if err != nil {
					return err
				}
				accounts[a.ID] = a
			}
			n := line.Native
			if a.CurrencyRevision == nil || n.Currency != a.Currency || !equalScale(n.Scale, a.Scale) ||
				n.AccountRevision != 0 && n.AccountRevision != *a.CurrencyRevision ||
				n.UnitDefinition != "" && n.UnitDefinition != a.CurrencyDefinition {
				return ErrInvalid
			}
			n.AccountRevision, n.UnitDefinition = *a.CurrencyRevision, a.CurrencyDefinition
		}
		for j := range line.Contexts {
			if !normalizeDenomination(&line.Contexts[j].Amount) {
				return ErrInvalid
			}
		}
		if line.Valuation == nil {
			input := money(book.Currency, book.Scale, line.AmountMinor)
			line.Valuation = &Valuation{Basis: "manual", Policy: "supplied-book-value-v1", Input: &input}
		}
		if line.Valuation.Input != nil {
			var valid bool
			if line.Valuation.Basis == "applied_quote" {
				// Quote bases describe a denomination, even when its code is
				// also used by a Book with a different historical precision.
				valid = normalizeDenomination(line.Valuation.Input)
			} else {
				valid = normalizeUnit(line.Valuation.Input, book)
			}
			if !valid {
				return ErrInvalid
			}
		}
	}
	if q.MeasurementPolicy != nil {
		for i := range q.MeasurementPolicy.Allocations {
			if !normalizeUnit(&q.MeasurementPolicy.Allocations[i].Total, book) {
				return ErrInvalid
			}
		}
	}
	if err := validateMeasurements(*q, book); err != nil {
		return err
	}
	for _, line := range q.Lines {
		if line.Valuation.Carrying != nil {
			uses := 0
			for _, other := range q.Lines {
				if other.AccountID == line.AccountID {
					uses++
				}
			}
			// One cash consumption per account and logical entry avoids
			// independently rounding several uses of the same position.
			if uses != 1 {
				return ErrInvalid
			}
			if err := validateCarrying(ctx, tx, s, book, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateMeasurements checks recorded inputs without consulting a current rate,
// display label or currency assignment. Database constraints repeat these rules.
func validateMeasurements(q PostJournal, book Book) error {
	realMovement := false
	for _, line := range q.Lines {
		amount, err := nonnegativeAmount(line.AmountMinor)
		if err != nil || len(line.Contexts) > 16 {
			return ErrInvalid
		}
		realMovement = realMovement || amount.Sign() != 0
		if n := line.Native; n != nil {
			if !validNativeUnit(n.Currency, n.Scale) || n.AccountRevision < 1 || n.UnitDefinition != currencyUnitVersion {
				return ErrInvalid
			}
			quantity, err := nonnegativeAmount(n.AmountMinor)
			if err != nil {
				return ErrInvalid
			}
			realMovement = realMovement || quantity.Sign() != 0
		}
		for _, value := range line.Contexts {
			if !rolePattern.MatchString(value.Role) || !validCurrencyAmount(value.Amount, true) ||
				!normalizeDenomination(&value.Amount) || len(value.Source) > 512 {
				return ErrInvalid
			}
		}
		v := line.Valuation
		if v == nil || len(v.Source) > 512 {
			return ErrInvalid
		}
		switch v.Basis {
		case "manual", "actual_source":
			policy := "supplied-book-value-v1"
			if v.Basis == "actual_source" {
				policy = "actual-source-book-value-v1"
			}
			if v.Policy != policy || v.Input == nil || !validCurrencyAmount(*v.Input, false) ||
				v.Input.Currency != book.Currency || *v.Input.Scale != book.Scale || v.Input.AmountMinor != line.AmountMinor ||
				v.Rate != nil || v.Carrying != nil || v.AllocationGroup != nil {
				return ErrInvalid
			}
		case "applied_quote":
			if v.Policy != "quoted-rate-half-even-v1" || v.Input == nil || v.Rate == nil || v.Carrying != nil || v.AllocationGroup != nil {
				return ErrInvalid
			}
			expected, err := convertedAmount(*v.Input, *v.Rate, book)
			if err != nil || expected != line.AmountMinor {
				return ErrInvalid
			}
		case "historical_carrying":
			if v.Policy != "moving-average-cash-v1" || v.Carrying == nil || v.Input != nil || v.Rate != nil || v.AllocationGroup != nil || line.Native == nil {
				return ErrInvalid
			}
		case "authoritative_allocation":
			if v.Policy != "largest-remainder-v1" || v.AllocationGroup == nil || v.Input != nil || v.Rate != nil || v.Carrying != nil {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	if !realMovement {
		return ErrInvalid
	}
	if q.MeasurementPolicy == nil {
		for _, line := range q.Lines {
			if line.Valuation.AllocationGroup != nil {
				return ErrInvalid
			}
		}
		return nil
	}
	p := q.MeasurementPolicy
	if p.Version != 1 || len(p.NativeTransfers) > 128 || len(p.Allocations) > 128 {
		return ErrInvalid
	}
	for _, group := range p.NativeTransfers {
		if len(group.Lines) < 2 || len(group.Lines) > 256 {
			return ErrInvalid
		}
		nativeTotal, bookTotal := new(big.Int), new(big.Int)
		seen := map[int]bool{}
		var unit *NativeMovement
		nonzero := false
		for _, position := range group.Lines {
			if position < 1 || position > len(q.Lines) || seen[position] {
				return ErrInvalid
			}
			seen[position] = true
			line := q.Lines[position-1]
			if line.Native == nil || unit != nil && (line.Native.Currency != unit.Currency || !equalScale(line.Native.Scale, unit.Scale)) {
				return ErrInvalid
			}
			unit = line.Native
			quantity, _ := nonnegativeAmount(unit.AmountMinor)
			nonzero = nonzero || quantity.Sign() != 0
			amount, _ := nonnegativeAmount(line.AmountMinor)
			if line.Side == "credit" {
				quantity.Neg(quantity)
				amount.Neg(amount)
			}
			nativeTotal.Add(nativeTotal, quantity)
			bookTotal.Add(bookTotal, amount)
		}
		if !nonzero || nativeTotal.Sign() != 0 || bookTotal.Sign() != 0 {
			return ErrInvalid
		}
	}
	allocated := map[int]int{}
	for i, group := range p.Allocations {
		shares, err := allocationShares(group, book)
		if err != nil {
			return err
		}
		var side string
		for position, share := range shares {
			if position > len(q.Lines) || allocated[position] != 0 {
				return ErrInvalid
			}
			line := q.Lines[position-1]
			if side != "" && side != line.Side || line.AmountMinor != share || line.Valuation.AllocationGroup == nil || *line.Valuation.AllocationGroup != i+1 {
				return ErrInvalid
			}
			side, allocated[position] = line.Side, i+1
		}
	}
	for i, line := range q.Lines {
		if line.Valuation.AllocationGroup != nil && allocated[i+1] != *line.Valuation.AllocationGroup {
			return ErrInvalid
		}
	}
	return nil
}

type nativeState struct {
	NativePosition
	BookMinor    string
	Role         string
	Class        string
	Revision     int64
	RoleRevision int64
}

func readNativeState(ctx context.Context, tx pgx.Tx, s Scope, account, through string, before int64) (nativeState, error) {
	var state nativeState
	err := tx.QueryRow(ctx, `SELECT status,coalesce(currency,''),native_scale,coalesce(native_minor::text,''),book_minor::text,last_sequence,
 coalesce(role,''),class,coalesce(currency_revision,0),coalesce(role_revision,0)
 FROM public.ledger_native_position($1,$2,$3,$4,$5)`, s.TenantID, s.BookID, account, through, before).
		Scan(&state.Status, &state.Currency, &state.Scale, &state.DebitPositiveMinor, &state.BookMinor, &state.ThroughSequence,
			&state.Role, &state.Class, &state.Revision, &state.RoleRevision)
	return state, err
}

func validateCarrying(ctx context.Context, tx pgx.Tx, s Scope, book Book, line PostLine) error {
	c := line.Valuation.Carrying
	state, err := readNativeState(ctx, tx, s, line.AccountID, "", 0)
	if err != nil {
		return err
	}
	if state.Status != "complete" || state.Class != "asset" || state.Role != "cash" || c.Policy != "moving-average-cash-v1" ||
		c.AccountID != line.AccountID || c.AccountRevision != state.Revision || c.RoleRevision != state.RoleRevision || c.BeforeSequence != state.ThroughSequence {
		return ErrInvalid
	}
	for _, value := range []*CurrencyAmount{&c.BeforeNative, &c.ConsumedNative} {
		if !validCurrencyAmount(*value, true) || !normalizeDenomination(value) {
			return ErrInvalid
		}
	}
	for _, value := range []*CurrencyAmount{&c.BeforeBook, &c.ConsumedBook} {
		if !validCurrencyAmount(*value, true) || !normalizeUnit(value, book) {
			return ErrInvalid
		}
	}
	if c.BeforeNative.Currency != state.Currency || !equalScale(c.BeforeNative.Scale, state.Scale) || c.BeforeNative.AmountMinor != state.DebitPositiveMinor ||
		c.BeforeBook.Currency != book.Currency || *c.BeforeBook.Scale != book.Scale || c.BeforeBook.AmountMinor != state.BookMinor ||
		!sameUnit(c.BeforeNative, c.ConsumedNative) || c.ConsumedNative.AmountMinor != line.Native.AmountMinor ||
		!sameUnit(c.BeforeBook, c.ConsumedBook) || c.ConsumedBook.AmountMinor != line.AmountMinor {
		return ErrInvalid
	}
	expected, err := carryingShare(state.DebitPositiveMinor, state.BookMinor, line.Native.AmountMinor)
	if err != nil || expected != line.AmountMinor || state.DebitPositiveMinor[0] == '-' && line.Side != "debit" ||
		state.DebitPositiveMinor[0] != '-' && line.Side != "credit" {
		return ErrInvalid
	}
	return nil
}

var snapshotOptionalColumns = map[string][]string{
	"book_revisions":    {"logo", "logo_actor"},
	"accounts":          {"currency", "native_scale", "currency_definition", "role", "currency_revision", "native_origin_revision", "role_revision"},
	"account_revisions": {"currency", "native_scale", "currency_definition", "role", "currency_revision", "native_origin_revision", "role_revision", "receipt_id"},
	"entries":           {"measurement_policy"},
	"lines":             {"native_currency", "native_scale", "native_amount_minor", "native_account_revision", "contexts", "valuation"},
}

func optionalSnapshotColumn(table, column string) bool {
	for _, name := range snapshotOptionalColumns[table] {
		if name == column {
			return true
		}
	}
	return false
}
func entryHasMeasurements(entry Entry) bool {
	if entry.MeasurementPolicy != nil {
		return true
	}
	for _, line := range entry.Lines {
		if line.Native != nil || line.Valuation != nil || len(line.Contexts) > 0 {
			return true
		}
	}
	return false
}
