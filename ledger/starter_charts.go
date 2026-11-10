package ledger

// StarterChartAccount is one account a starter chart creates. Parent names
// the code of an account earlier in the same chart.
type StarterChartAccount struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Class  string `json:"class"`
	Role   string `json:"role,omitempty"`
	Parent string `json:"parent,omitempty"`
	// Heading accounts group others and take no postings.
	Heading bool `json:"heading,omitempty"`
}

// StarterChartTemplate is a versioned, fixed list of ordinary accounts. A
// changed chart is a new version; a recorded version never changes.
type StarterChartTemplate struct {
	ID          string                `json:"id"`
	Profile     string                `json:"profile"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Accounts    []StarterChartAccount `json:"accounts"`
}

var starterCharts = []StarterChartTemplate{
	{ID: "business-v1", Profile: "business", Title: "Business",
		Description: "Accounts for a small business: bank and card, what customers owe and what the business owes, sales tax collected, revenue, cost of sales and everyday operating costs.",
		Accounts: []StarterChartAccount{
			{Code: "1000", Name: "Assets", Class: "asset", Heading: true},
			{Code: "1100", Name: "Bank accounts", Class: "asset", Parent: "1000", Heading: true},
			{Code: "1110", Name: "Checking", Class: "asset", Role: "cash", Parent: "1100"},
			{Code: "1120", Name: "Savings", Class: "asset", Role: "cash", Parent: "1100"},
			{Code: "1200", Name: "Accounts receivable", Class: "asset", Role: "receivable", Parent: "1000"},
			{Code: "1500", Name: "Equipment", Class: "asset", Role: "fixed_asset", Parent: "1000"},
			{Code: "2000", Name: "Liabilities", Class: "liability", Heading: true},
			{Code: "2100", Name: "Credit card", Class: "liability", Role: "card", Parent: "2000"},
			{Code: "2200", Name: "Accounts payable", Class: "liability", Role: "payable", Parent: "2000"},
			{Code: "2300", Name: "Loans", Class: "liability", Role: "loan", Parent: "2000"},
			{Code: "2400", Name: "Sales tax payable", Class: "liability", Parent: "2000"},
			{Code: "3000", Name: "Equity", Class: "equity", Heading: true},
			{Code: "3100", Name: "Owner contributions", Class: "equity", Parent: "3000"},
			{Code: "3200", Name: "Owner withdrawals", Class: "equity", Parent: "3000"},
			{Code: "3800", Name: "Accumulated earnings", Class: "equity", Parent: "3000"},
			{Code: "3900", Name: "Opening balances", Class: "equity", Role: "opening_balance", Parent: "3000"},
			{Code: "4000", Name: "Revenue", Class: "income", Heading: true},
			{Code: "4100", Name: "Sales", Class: "income", Parent: "4000"},
			{Code: "4900", Name: "Other income", Class: "income", Parent: "4000"},
			{Code: "5000", Name: "Cost of sales", Class: "expense", Role: "cost_of_sales"},
			{Code: "6000", Name: "Operating expenses", Class: "expense", Heading: true},
			{Code: "6050", Name: "Advertising and marketing", Class: "expense", Parent: "6000"},
			{Code: "6100", Name: "Rent", Class: "expense", Parent: "6000"},
			{Code: "6200", Name: "Software and subscriptions", Class: "expense", Parent: "6000"},
			{Code: "6300", Name: "Professional fees", Class: "expense", Parent: "6000"},
			{Code: "6400", Name: "Travel", Class: "expense", Parent: "6000"},
			{Code: "6500", Name: "Meals", Class: "expense", Parent: "6000"},
			{Code: "6600", Name: "Bank and card fees", Class: "expense", Parent: "6000"},
			{Code: "6700", Name: "Payroll", Class: "expense", Parent: "6000"},
			{Code: "6800", Name: "Taxes and licenses", Class: "expense", Parent: "6000"},
			{Code: "6900", Name: "Insurance", Class: "expense", Parent: "6000"},
			{Code: "7000", Name: "Interest expense", Class: "expense"},
		}},
	{ID: "personal-v1", Profile: "personal", Title: "Personal",
		Description: "Accounts for household money: bank, savings and card, investments, property and loans, income and where money goes each month.",
		Accounts: []StarterChartAccount{
			{Code: "1000", Name: "Assets", Class: "asset", Heading: true},
			{Code: "1100", Name: "Checking", Class: "asset", Role: "cash", Parent: "1000"},
			{Code: "1200", Name: "Savings", Class: "asset", Role: "cash", Parent: "1000"},
			{Code: "1300", Name: "Investments", Class: "asset", Role: "investment", Parent: "1000"},
			{Code: "1400", Name: "Property", Class: "asset", Role: "fixed_asset", Parent: "1000"},
			{Code: "2000", Name: "Liabilities", Class: "liability", Heading: true},
			{Code: "2100", Name: "Credit card", Class: "liability", Role: "card", Parent: "2000"},
			{Code: "2200", Name: "Loans", Class: "liability", Role: "loan", Parent: "2000"},
			{Code: "3000", Name: "Net worth", Class: "equity", Heading: true},
			{Code: "3100", Name: "Opening balances", Class: "equity", Role: "opening_balance", Parent: "3000"},
			{Code: "4000", Name: "Income", Class: "income", Heading: true},
			{Code: "4100", Name: "Salary", Class: "income", Parent: "4000"},
			{Code: "4200", Name: "Interest", Class: "income", Parent: "4000"},
			{Code: "4900", Name: "Other income", Class: "income", Parent: "4000"},
			{Code: "5000", Name: "Expenses", Class: "expense", Heading: true},
			{Code: "5100", Name: "Home", Class: "expense", Parent: "5000", Heading: true},
			{Code: "5110", Name: "Rent", Class: "expense", Parent: "5100"},
			{Code: "5120", Name: "Utilities", Class: "expense", Parent: "5100"},
			{Code: "5130", Name: "Mortgage interest", Class: "expense", Parent: "5100"},
			{Code: "5200", Name: "Groceries", Class: "expense", Parent: "5000"},
			{Code: "5300", Name: "Eating out", Class: "expense", Parent: "5000"},
			{Code: "5400", Name: "Transport", Class: "expense", Parent: "5000"},
			{Code: "5500", Name: "Health", Class: "expense", Parent: "5000"},
			{Code: "5600", Name: "Subscriptions", Class: "expense", Parent: "5000"},
			{Code: "5700", Name: "Travel", Class: "expense", Parent: "5000"},
			{Code: "5750", Name: "Insurance", Class: "expense", Parent: "5000"},
			{Code: "5800", Name: "Gifts and donations", Class: "expense", Parent: "5000"},
			{Code: "5850", Name: "Income taxes", Class: "expense", Parent: "5000"},
			{Code: "5880", Name: "Other interest expense", Class: "expense", Parent: "5000"},
			{Code: "5900", Name: "Bank and card fees", Class: "expense", Parent: "5000"},
		}},
}

// StarterCharts lists the available starter charts.
func StarterCharts() []StarterChartTemplate {
	return append([]StarterChartTemplate{}, starterCharts...)
}

// StarterChart returns one starter chart by its versioned ID.
func StarterChart(id string) (StarterChartTemplate, bool) {
	for _, chart := range starterCharts {
		if chart.ID == id {
			return chart, true
		}
	}
	return StarterChartTemplate{}, false
}
