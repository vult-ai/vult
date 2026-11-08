package ledger

import (
	_ "embed"
	"encoding/json"
	"slices"
	"sort"
)

//go:embed currencies.json
var currencyCatalog []byte

const currencyUnitVersion = "iso4217-2026-09-17"

type Currency struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale int    `json:"scale"`
}

var currencies = func() []Currency {
	var catalog struct {
		Currencies []Currency `json:"currencies"`
	}
	if err := json.Unmarshal(currencyCatalog, &catalog); err != nil {
		panic(err)
	}
	return catalog.Currencies
}()

func Currencies() []Currency { return slices.Clone(currencies) }

func CurrencyScale(code string) (int, bool) {
	i := sort.Search(len(currencies), func(i int) bool { return currencies[i].Code >= code })
	if i == len(currencies) || currencies[i].Code != code {
		return 0, false
	}
	return currencies[i].Scale, true
}
