package webhook

// EventPayload is the JSON payload sent to webhook endpoints.
type EventPayload struct {
	Network     string `json:"network"`
	Currency    string `json:"currency"`
	TxID        string `json:"txid"`
	Block       int64  `json:"block,omitempty"`
	Ts          string `json:"ts"`
	Address     string `json:"address"`
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	AmountRaw   string `json:"amount_raw,omitempty"`
	Fee         string `json:"fee,omitempty"`
	FeeCurrency string `json:"fee_currency,omitempty"`
	Direction   string `json:"direction"`
}
