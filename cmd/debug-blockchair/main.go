// Blockchair API v2 — debug / exploration script.
//
// Supported by Blockchair API v2 (from official docs):
//
//	EVM  : ethereum
//	UTXO : bitcoin, bitcoin-cash, litecoin, bitcoin-sv, dogecoin, dash,
//	        groestlcoin, zcash, ecash
//	Other: ripple  → /raw/account  (balance only, no tx history)
//	       cardano → /raw/address  (balance + tx list)
//	       stellar, monero, tezos, eos → raw endpoints only
//
// NOT supported (HTML 404): bnb, bsc, polygon, solana — they appear on the
// blockchair.com website UI but are NOT in API v2.
// Blockchair's notice points to https://3xpl.com/data for their new API v3.
//
// Usage:
//
//	go run ./cmd/debug-blockchair/ -network ethereum  -address 0x... -key YOUR_KEY
//	go run ./cmd/debug-blockchair/ -network bitcoin   -address bc1q... -key YOUR_KEY
//	go run ./cmd/debug-blockchair/ -network ripple    -address r9cZ... -key YOUR_KEY
//	go run ./cmd/debug-blockchair/ -network cardano   -address addr1q... -key YOUR_KEY
//	go run ./cmd/debug-blockchair/ -network ethereum  -address 0x... -key YOUR_KEY -raw
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const apiBase = "https://api.blockchair.com"

// ── network classification ─────────────────────────────────────────────────────

var evmNets = map[string]bool{
	"ethereum": true, "ethereum/testnet": true,
}
var utxoNets = map[string]bool{
	"bitcoin": true, "bitcoin-cash": true, "litecoin": true, "bitcoin-sv": true,
	"dogecoin": true, "dash": true, "groestlcoin": true, "zcash": true,
	"ecash": true, "bitcoin/testnet": true,
}
var unsupportedNets = map[string]bool{
	"bnb": true, "bsc": true, "polygon": true, "solana": true,
}

func main() {
	address := flag.String("address", "", "wallet address (required)")
	network := flag.String("network", "ethereum", "blockchair network slug")
	apiKey := flag.String("key", "", "blockchair API key")
	rawMode := flag.Bool("raw", false, "print raw JSON")
	flag.Parse()

	if *address == "" {
		fmt.Fprintln(os.Stderr, "usage: debug-blockchair -address <wallet address> [-network ethereum] [-key <api key>] [-raw]")
		os.Exit(2)
	}

	fmt.Printf("Network : %s\n", *network)
	fmt.Printf("Address : %s\n", *address)
	fmt.Printf("API key : %s\n", maskKey(*apiKey))
	fmt.Println(strings.Repeat("─", 64))

	switch {
	case unsupportedNets[*network]:
		fmt.Printf("✗ Network %q is NOT in Blockchair API v2.\n", *network)
		fmt.Println("  (It appears on the website but the API returns HTML 404.)")
		fmt.Println("  Blockchair's note in responses: \"Try out API v.3: https://3xpl.com/data\"")
		fmt.Println("  For BNB/Polygon monitoring use BSCScan / Moralis / Alchemy APIs instead.")
		os.Exit(1)

	case evmNets[*network]:
		runEVM(*network, *address, *apiKey, *rawMode)

	case utxoNets[*network]:
		runUTXO(*network, *address, *apiKey, *rawMode)

	case *network == "ripple":
		runRipple(*address, *apiKey, *rawMode)

	case *network == "cardano":
		runCardano(*address, *apiKey, *rawMode)

	default:
		fmt.Printf("Network %q: raw-only or unknown. Use -raw flag to inspect.\n", *network)
		runGenericRaw(*network+"/raw/account/"+*address, *apiKey)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// EVM (Ethereum)
// endpoint : /{network}/dashboards/address/{address}
// response : { address: {...}, calls: [{value(wei), sender, recipient, time, tx_hash}] }
// NOTE     : `calls` = internal ETH transfers only. Value is in WEI → divide by 1e18.
//            ERC-20 token transfers are NOT included; use /erc-20/dashboards/address/ instead.
// ═══════════════════════════════════════════════════════════════════════════════

type evmResp struct {
	Data    map[string]evmData `json:"data"`
	Context apiCtx             `json:"context"`
}
type evmData struct {
	Address struct {
		Type               string  `json:"type"`
		Balance            string  `json:"balance"` // wei, as string
		BalanceUsd         float64 `json:"balance_usd"`
		ReceivedUsd        float64 `json:"received_usd"`
		FeesApprox         string  `json:"fees_approximate"`
		CallCount          int     `json:"call_count"`
		ReceivingCallCount int     `json:"receiving_call_count"`
		SpendingCallCount  int     `json:"spending_call_count"`
		TransactionCount   int     `json:"transaction_count"`
		FirstSeenReceiving string  `json:"first_seen_receiving"`
		LastSeenReceiving  string  `json:"last_seen_receiving"`
	} `json:"address"`
	Calls []struct {
		BlockID         int     `json:"block_id"`
		TransactionHash string  `json:"transaction_hash"`
		Index           string  `json:"index"`
		Time            string  `json:"time"`
		Sender          string  `json:"sender"`
		Recipient       string  `json:"recipient"`
		Value           float64 `json:"value"` // wei (integer stored as float64)
		ValueUsd        float64 `json:"value_usd"`
		Transferred     bool    `json:"transferred"`
	} `json:"calls"`
}

func runEVM(network, address, apiKey string, rawMode bool) {
	path := network + "/dashboards/address/" + url.PathEscape(address)
	body, status := get(path, apiKey)

	fmt.Printf("HTTP %d\n\n", status)
	if rawMode {
		printJSON(body)
		return
	}
	if !checkStatus(status, body) {
		return
	}

	var r evmResp
	mustUnmarshal(body, &r)
	if !checkCtx(r.Context) {
		return
	}

	data, ok := findCaseInsensitive(r.Data, address)
	if !ok {
		fmt.Printf("Address not found in response data. Keys: %v\n", mapKeys(r.Data))
		return
	}

	a := data.Address
	fmt.Printf("Type              : %s\n", a.Type)
	fmt.Printf("Balance           : %s wei  (~%.8f ETH)  $%.4f\n",
		a.Balance, parseWei(a.Balance), a.BalanceUsd)
	fmt.Printf("Received USD      : $%.4f\n", a.ReceivedUsd)
	fmt.Printf("Tx count          : %d\n", a.TransactionCount)
	fmt.Printf("Call count        : %d  (in: %d  out: %d)\n",
		a.CallCount, a.ReceivingCallCount, a.SpendingCallCount)
	fmt.Printf("First seen        : %s\n", a.FirstSeenReceiving)
	fmt.Printf("Last seen rcv     : %s\n", a.LastSeenReceiving)

	fmt.Printf("\n── calls (%d) ──  [value in wei → ÷1e18 = ETH]\n", len(data.Calls))
	fmt.Printf("   note: these are internal ETH transfers only, NOT ERC-20 token transfers\n\n")
	for i, c := range data.Calls {
		dir := dirOf(c.Sender, c.Recipient, address)
		eth := c.Value / 1e18
		fmt.Printf("[%2d] %s  %-3s  %s → %s  wei=%.0f  ETH=%.10f  $%.4f\n",
			i, c.Time, dir, short(c.Sender), short(c.Recipient),
			c.Value, eth, c.ValueUsd)
	}

	// Also fetch ERC-20 tokens (USDT, USDC).
	runAllERC20(address, apiKey, rawMode)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ERC-20 tokens (Ethereum)
// endpoint : /ethereum/erc-20/{contract}/dashboards/address/{address}
// response : { address: {balance, tx_count}, transactions: [{id, tx_hash, time, sender,
//              recipient, value(string raw), value_approximate, token_symbol, token_decimals}] }
// NOTE     : each event is returned TWICE by Blockchair — dedup by `id`.
//            value=="0" entries are approve() calls, not real transfers.
// ═══════════════════════════════════════════════════════════════════════════════

type erc20Resp struct {
	Data    map[string]erc20Data `json:"data"`
	Context apiCtx               `json:"context"`
}
type erc20Data struct {
	Address struct {
		Balance          string  `json:"balance"`
		BalanceApprox    float64 `json:"balance_approximate"`
		TransactionCount int     `json:"transaction_count"`
	} `json:"address"`
	Transactions []struct {
		BlockID         int     `json:"block_id"`
		ID              int64   `json:"id"`
		TransactionHash string  `json:"transaction_hash"`
		Time            string  `json:"time"`
		TokenAddress    string  `json:"token_address"`
		TokenSymbol     string  `json:"token_symbol"`
		TokenDecimals   int     `json:"token_decimals"`
		Sender          string  `json:"sender"`
		Recipient       string  `json:"recipient"`
		Value           string  `json:"value"`
		ValueApprox     float64 `json:"value_approximate"`
	} `json:"transactions"`
}

var knownERC20 = []struct {
	contract string
	symbol   string
}{
	{"0xdAC17F958D2ee523a2206206994597C13D831ec7", "USDT"},
	{"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", "USDC"},
}

func runAllERC20(address, apiKey string, rawMode bool) {
	for _, tok := range knownERC20 {
		fmt.Printf("\n%s\n", strings.Repeat("═", 64))
		fmt.Printf("ERC-20 %s  contract=%s\n", tok.symbol, tok.contract)
		fmt.Println(strings.Repeat("─", 64))
		runERC20(tok.contract, tok.symbol, address, apiKey, rawMode)
	}
}

func runERC20(contract, symbol, address, apiKey string, rawMode bool) {
	path := "ethereum/erc-20/" + contract + "/dashboards/address/" + url.PathEscape(address)
	body, status := get(path, apiKey)

	fmt.Printf("HTTP %d\n\n", status)
	if rawMode {
		printJSON(body)
		return
	}
	if !checkStatus(status, body) {
		return
	}

	var r erc20Resp
	mustUnmarshal(body, &r)
	if !checkCtx(r.Context) {
		return
	}

	data, ok := findCaseInsensitive(r.Data, address)
	if !ok {
		fmt.Printf("Address not found in response. Keys: %v\n", mapKeys(r.Data))
		return
	}

	a := data.Address
	fmt.Printf("Token             : %s\n", symbol)
	fmt.Printf("Balance (raw)     : %s\n", a.Balance)
	fmt.Printf("Balance           : %.6f %s\n", a.BalanceApprox, symbol)
	fmt.Printf("Tx count          : %d\n", a.TransactionCount)

	// Deduplicate by ID (Blockchair returns each event twice).
	seen := make(map[int64]struct{}, len(data.Transactions))
	var uniq []struct {
		BlockID         int
		ID              int64
		TransactionHash string
		Time            string
		TokenAddress    string
		TokenSymbol     string
		TokenDecimals   int
		Sender          string
		Recipient       string
		Value           string
		ValueApprox     float64
	}
	for _, tx := range data.Transactions {
		if _, dup := seen[tx.ID]; dup {
			continue
		}
		seen[tx.ID] = struct{}{}
		uniq = append(uniq, struct {
			BlockID         int
			ID              int64
			TransactionHash string
			Time            string
			TokenAddress    string
			TokenSymbol     string
			TokenDecimals   int
			Sender          string
			Recipient       string
			Value           string
			ValueApprox     float64
		}{
			tx.BlockID, tx.ID, tx.TransactionHash, tx.Time,
			tx.TokenAddress, tx.TokenSymbol, tx.TokenDecimals,
			tx.Sender, tx.Recipient, tx.Value, tx.ValueApprox,
		})
	}

	fmt.Printf("\n── %s transactions (%d unique, %d raw) ──\n", symbol, len(uniq), len(data.Transactions))
	fmt.Printf("   note: value==\"0\" = approve() call (skipped in poller)\n\n")

	skipped := 0
	for i, tx := range uniq {
		if tx.Value == "0" || tx.Value == "" {
			skipped++
			continue
		}
		dir := dirOf(tx.Sender, tx.Recipient, address)
		fmt.Printf("[%2d] id=%-12d  %s  %-3s  %s → %s  amount=%.4f %s  tx=%s\n",
			i, tx.ID, tx.Time, dir, short(tx.Sender), short(tx.Recipient),
			tx.ValueApprox, symbol, short(tx.TransactionHash))
	}
	if skipped > 0 {
		fmt.Printf("     (%d approve() calls skipped)\n", skipped)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// UTXO (Bitcoin-like)
// endpoint : /{network}/dashboards/address/{address}
// response : { address: {...}, transactions: [hash,...], utxo: [...] }
// NOTE     : transactions = array of tx HASHES only. Use
//            /{network}/dashboards/transactions/{hash,...} to get full details.
// ═══════════════════════════════════════════════════════════════════════════════

type utxoResp struct {
	Data    map[string]utxoData `json:"data"`
	Context apiCtx              `json:"context"`
}
type utxoData struct {
	Address struct {
		Type               string  `json:"type"`
		Balance            float64 `json:"balance"` // satoshi
		BalanceUsd         float64 `json:"balance_usd"`
		Received           float64 `json:"received"`
		Spent              float64 `json:"spent"`
		OutputCount        int     `json:"output_count"`
		UnspentOutputCount int     `json:"unspent_output_count"`
		TransactionCount   int     `json:"transaction_count"`
		FirstSeenReceiving string  `json:"first_seen_receiving"`
		LastSeenReceiving  string  `json:"last_seen_receiving"`
	} `json:"address"`
	Transactions []string `json:"transactions"` // hashes only!
	Utxo         []struct {
		BlockID         int    `json:"block_id"`
		TransactionHash string `json:"transaction_hash"`
		Index           int    `json:"index"`
		Value           int64  `json:"value"` // satoshi
	} `json:"utxo"`
}

func runUTXO(network, address, apiKey string, rawMode bool) {
	path := network + "/dashboards/address/" + url.PathEscape(address)
	body, status := get(path, apiKey)

	fmt.Printf("HTTP %d\n\n", status)
	if rawMode {
		printJSON(body)
		return
	}
	if !checkStatus(status, body) {
		return
	}

	var r utxoResp
	mustUnmarshal(body, &r)
	if !checkCtx(r.Context) {
		return
	}

	data, ok := findCaseInsensitive(r.Data, address)
	if !ok {
		fmt.Printf("Address not found in response. Keys: %v\n", mapKeys(r.Data))
		return
	}

	a := data.Address
	fmt.Printf("Type              : %s\n", a.Type)
	fmt.Printf("Balance (sat)     : %.0f  (~%.8f BTC)  $%.4f\n",
		a.Balance, a.Balance/1e8, a.BalanceUsd)
	fmt.Printf("Received (sat)    : %.0f\n", a.Received)
	fmt.Printf("Spent (sat)       : %.0f\n", a.Spent)
	fmt.Printf("UTXO              : %d total / %d unspent\n",
		a.OutputCount, a.UnspentOutputCount)
	fmt.Printf("Tx count          : %d\n", a.TransactionCount)
	fmt.Printf("First seen        : %s\n", a.FirstSeenReceiving)
	fmt.Printf("Last seen rcv     : %s\n", a.LastSeenReceiving)

	fmt.Printf("\n── transactions (%d hashes) ──  [need /dashboards/transactions/ for details]\n",
		len(data.Transactions))
	for i, h := range data.Transactions {
		if i >= 10 {
			fmt.Printf("  ... +%d more\n", len(data.Transactions)-10)
			break
		}
		fmt.Printf("  [%d] %s\n", i, h)
	}

	fmt.Printf("\n── utxo (%d) ──\n", len(data.Utxo))
	for i, u := range data.Utxo {
		fmt.Printf("  [%d] block=%-8d  sat=%-12d  (~%.8f)  tx=%s idx=%d\n",
			i, u.BlockID, u.Value, float64(u.Value)/1e8, short(u.TransactionHash), u.Index)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Ripple  — endpoint: /ripple/raw/account/{address}
// Returns: account_data.Balance (in drops, 1 XRP = 1_000_000 drops), no tx list.
// ═══════════════════════════════════════════════════════════════════════════════

type rippleResp struct {
	Data    map[string]rippleData `json:"data"`
	Context apiCtx                `json:"context"`
}
type rippleData struct {
	Account struct {
		AccountData struct {
			Account  string `json:"Account"`
			Balance  string `json:"Balance"` // drops
			Sequence int    `json:"Sequence"`
		} `json:"account_data"`
	} `json:"account"`
	Transactions interface{} `json:"transactions"` // null — no history
}

func runRipple(address, apiKey string, rawMode bool) {
	path := "ripple/raw/account/" + url.PathEscape(address)
	body, status := get(path, apiKey)

	fmt.Printf("HTTP %d\n\n", status)
	if rawMode {
		printJSON(body)
		return
	}
	if !checkStatus(status, body) {
		return
	}

	var r rippleResp
	mustUnmarshal(body, &r)
	if !checkCtx(r.Context) {
		return
	}

	data, ok := findCaseInsensitive(r.Data, address)
	if !ok {
		fmt.Printf("Address not found. Keys: %v\n", mapKeys(r.Data))
		return
	}

	acc := data.Account.AccountData
	var balDrops float64
	fmt.Sscanf(acc.Balance, "%f", &balDrops)

	fmt.Printf("Account           : %s\n", acc.Account)
	fmt.Printf("Balance (drops)   : %s  (~%.6f XRP)\n", acc.Balance, balDrops/1e6)
	fmt.Printf("Sequence          : %d\n", acc.Sequence)
	fmt.Println("\nNOTE: Ripple raw/account returns NO transaction history.")
	fmt.Println("      For tx history use Ripple's own API or a different provider.")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Cardano — endpoint: /cardano/raw/address/{address}
// Returns: caBalance.getCoin (lovelace, 1 ADA = 1_000_000 lovelace), caTxList.
// ═══════════════════════════════════════════════════════════════════════════════

type cardanoResp struct {
	Data    map[string]cardanoData `json:"data"`
	Context apiCtx                 `json:"context"`
}
type cardanoData struct {
	Address struct {
		CaAddress string `json:"caAddress"`
		CaBalance struct {
			GetCoin string `json:"getCoin"` // lovelace as string
		} `json:"caBalance"`
		CaTxNum  int `json:"caTxNum"`
		CaTxList []struct {
			CtbId         string                   `json:"ctbId"`
			CtbTimeIssued int64                    `json:"ctbTimeIssued"`
			CtbInputSum   struct{ GetCoin string } `json:"ctbInputSum"`
			CtbOutputSum  struct{ GetCoin string } `json:"ctbOutputSum"`
		} `json:"caTxList"`
	} `json:"address"`
}

func runCardano(address, apiKey string, rawMode bool) {
	path := "cardano/raw/address/" + url.PathEscape(address)
	body, status := get(path, apiKey)

	fmt.Printf("HTTP %d\n\n", status)
	if rawMode {
		printJSON(body)
		return
	}
	if !checkStatus(status, body) {
		return
	}

	var r cardanoResp
	mustUnmarshal(body, &r)
	if !checkCtx(r.Context) {
		return
	}

	data, ok := findCaseInsensitive(r.Data, address)
	if !ok {
		fmt.Printf("Address not found. Keys: %v\n", mapKeys(r.Data))
		return
	}

	a := data.Address
	var lovelace float64
	fmt.Sscanf(a.CaBalance.GetCoin, "%f", &lovelace)

	fmt.Printf("Address   : %s\n", a.CaAddress)
	fmt.Printf("Balance   : %s lovelace  (~%.6f ADA)\n", a.CaBalance.GetCoin, lovelace/1e6)
	fmt.Printf("Tx count  : %d\n", a.CaTxNum)

	fmt.Printf("\n── transactions (%d) ──\n", len(a.CaTxList))
	for i, tx := range a.CaTxList {
		fmt.Printf("  [%d] %s  time=%d\n", i, tx.CtbId, tx.CtbTimeIssued)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Generic raw dump (stellar, monero, etc.)
// ═══════════════════════════════════════════════════════════════════════════════

func runGenericRaw(path, apiKey string) {
	body, status := get(path, apiKey)
	fmt.Printf("HTTP %d\n\n", status)
	printJSON(body)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Shared helpers
// ═══════════════════════════════════════════════════════════════════════════════

type apiCtx struct {
	Code    int    `json:"code"`
	Error   string `json:"error"`
	Results int    `json:"results"`
}

func get(path, apiKey string) ([]byte, int) {
	reqURL := apiBase + "/" + path
	if apiKey != "" {
		reqURL += "?key=" + apiKey
	}
	fmt.Printf("GET %s\n", reqURL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	req.Header.Set("User-Agent", "cleanroom-watcher-debug/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HTTP error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode
}

func checkStatus(status int, body []byte) bool {
	if status == 430 {
		fmt.Println("IP BLACKLISTED (430) — too many requests without API key.")
		return false
	}
	if status != 200 {
		fmt.Printf("Non-200 response (%d):\n%s\n", status, body)
		return false
	}
	return true
}

func checkCtx(ctx apiCtx) bool {
	if ctx.Code != 0 && ctx.Code != 200 {
		fmt.Printf("API error (code %d): %s\n", ctx.Code, ctx.Error)
		return false
	}
	return true
}

func mustUnmarshal(body []byte, v interface{}) {
	if err := json.Unmarshal(body, v); err != nil {
		fmt.Fprintf(os.Stderr, "JSON unmarshal error: %v\nBody: %s\n", err, body)
		os.Exit(1)
	}
}

func printJSON(b []byte) {
	var v interface{}
	if json.Unmarshal(b, &v) == nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(v) //nolint
	} else {
		fmt.Printf("%s\n", b)
	}
}

func findCaseInsensitive[V any](m map[string]V, key string) (V, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	lk := strings.ToLower(key)
	for k, v := range m {
		if strings.ToLower(k) == lk {
			return v, true
		}
	}
	var zero V
	return zero, false
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func dirOf(sender, recipient, watched string) string {
	if strings.EqualFold(recipient, watched) {
		return "IN"
	}
	return "OUT"
}

func short(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:8] + "…" + s[len(s)-6:]
}

func parseWei(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f / 1e18
}

func maskKey(k string) string {
	if k == "" {
		return "(none — requests will be rate-limited by IP)"
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + strings.Repeat("*", len(k)-8) + k[len(k)-4:]
}
