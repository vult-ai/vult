package mcpserver

import "encoding/json"

func addMeasurementProperties(method string, properties map[string]json.RawMessage) {
	switch method {
	case "create_account", "revise_account":
		properties["currency"] = json.RawMessage(`{"type":"string","pattern":"^[A-Z]{3}$","description":"Explicit account denomination; paired with scale. It never supplies missing historical movements."}`)
		properties["scale"] = json.RawMessage(`{"type":"integer","minimum":0,"maximum":4,"description":"The currency catalog's native precision, independent of the Book precision."}`)
		properties["role"] = json.RawMessage(`{"type":"string","pattern":"^([a-z][a-z0-9_]{0,63})?$","description":"Optional accepted accounting role. Cash carrying allocation uses asset accounts with the cash role. Use receivable for an asset representing an amount owed to this Book."}`)
		if method == "create_account" {
			properties["native_origin"] = json.RawMessage(`{"type":"string","enum":["empty_ledger"],"description":"Explicitly begin this new ledger account at zero native position. Omit for accounts whose opening is not established."}`)
		}
	case "post_journal":
		var lines struct {
			Type     string `json:"type"`
			MinItems int    `json:"minItems"`
			MaxItems int    `json:"maxItems"`
			Items    struct {
				Type                 string                     `json:"type"`
				AdditionalProperties bool                       `json:"additionalProperties"`
				Required             []string                   `json:"required"`
				Properties           map[string]json.RawMessage `json:"properties"`
			} `json:"items"`
		}
		if json.Unmarshal(properties["lines"], &lines) != nil {
			panic("invalid journal schema")
		}
		lines.Items.Properties["native"] = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["currency","scale","amount_minor"],"properties":{"currency":{"type":"string","pattern":"^[A-Z]{3}$"},"scale":{"type":"integer","minimum":0,"maximum":4},"amount_minor":{"type":"string","pattern":"^(0|[1-9][0-9]{0,18})$"},"account_revision":{"type":"integer","minimum":1},"unit_definition":{"type":"string","enum":["iso4217-2026-09-17"]}}}`)
		lines.Items.Properties["contexts"] = json.RawMessage(`{"type":"array","maxItems":16,"items":{"type":"object","additionalProperties":false,"required":["role","amount"],"properties":{"role":{"type":"string","pattern":"^[a-z][a-z0-9_]{0,63}$"},"amount":` + moneySchema + `,"source":{"type":"string","maxLength":512}}}}`)
		lines.Items.Properties["valuation"] = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["basis","policy"],"properties":{"basis":{"type":"string","enum":["manual","actual_source","applied_quote","historical_carrying","authoritative_allocation"]},"policy":{"type":"string"},"input":` + moneySchema + `,"source":{"type":"string","maxLength":512},"rate":{"type":"object","additionalProperties":false,"required":["base","quote","rate","requested_date","observation_date","provenance"],"properties":{"base":{"type":"string"},"quote":{"type":"string"},"rate":{"type":"string","pattern":"^(0|[1-9][0-9]{0,63})(\\.[0-9]{1,64})?$"},"requested_date":{"type":"string","format":"date"},"observation_date":{"type":"string","format":"date"},"provenance":{"type":"string","minLength":1,"maxLength":512}}},"allocation_group":{"type":"integer","minimum":1,"maximum":128},"carrying":{"type":"object","additionalProperties":false,"required":["policy","account_id","account_revision","role_revision","before_sequence","before_native","before_book","consumed_native","consumed_book"],"properties":{"policy":{"type":"string","enum":["moving-average-cash-v1"]},"account_id":{"type":"string"},"account_revision":{"type":"integer","minimum":1},"role_revision":{"type":"integer","minimum":1},"before_sequence":{"type":"integer","minimum":1},"before_native":` + moneySchema + `,"before_book":` + moneySchema + `,"consumed_native":` + moneySchema + `,"consumed_book":` + moneySchema + `}}}}`)
		encoded, err := json.Marshal(lines)
		if err != nil {
			panic("invalid measurement schema")
		}
		properties["lines"] = encoded
		properties["measurement_policy"] = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version"],"properties":{"version":{"type":"integer","enum":[1]},"native_transfers":{"type":"array","maxItems":128,"items":{"type":"object","additionalProperties":false,"required":["lines"],"properties":{"lines":{"type":"array","minItems":2,"maxItems":256,"items":{"type":"integer","minimum":1,"maximum":256}}}}},"allocations":{"type":"array","maxItems":128,"items":{"type":"object","additionalProperties":false,"required":["total","method","parts"],"properties":{"total":` + moneySchema + `,"method":{"type":"string","enum":["largest-remainder-v1"]},"parts":{"type":"array","minItems":1,"maxItems":256,"items":{"type":"object","additionalProperties":false,"required":["line","weight"],"properties":{"line":{"type":"integer","minimum":1,"maximum":256},"weight":{"type":"string","pattern":"^[1-9][0-9]{0,18}$"}}}}}}}}}`)
	}
}

const moneySchema = `{"type":"object","additionalProperties":false,"required":["currency","scale","amount_minor"],"properties":{"currency":{"type":"string","pattern":"^[A-Z]{3}$"},"scale":{"type":"integer","minimum":0,"maximum":4},"amount_minor":{"type":"string","pattern":"^(0|-?[1-9][0-9]{0,18})$"},"unit_definition":{"type":"string","enum":["book","iso4217-2026-09-17"]}}}`
