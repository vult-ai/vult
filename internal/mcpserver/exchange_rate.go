package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/vult-ai/vult/internal/fxrates"
	"github.com/vult-ai/vult/internal/strictjson"
)

// rates is replaced in tests so they never depend on the public provider.
var rates = fxrates.Client{}

// exchangeRate answers get_exchange_rate without touching the Book. Only the
// currency pair and the date leave the installation.
func exchangeRate(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var q struct {
		Base  string `json:"base"`
		Quote string `json:"quote"`
		Date  string `json:"date"`
	}
	if strictjson.Decode(input, &q) != nil {
		return nil, fxrates.ErrInvalid
	}
	rate, err := rates.Lookup(ctx, q.Base, q.Quote, q.Date)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rate)
}
