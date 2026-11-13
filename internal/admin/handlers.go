package admin

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/db"
)

// handleAddresses renders the address list page.
func (h *Handler) handleAddresses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	network := q.Get("network")
	showAll := q.Get("show") == "all"

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	const perPage = 50
	offset := (page - 1) * perPage

	var activeFilter *bool
	if !showAll {
		t := true
		activeFilter = &t
	}

	addresses, total, err := db.ListWatched(r.Context(), h.db, perPage, offset, activeFilter, network, search)
	if err != nil {
		slog.Error("admin: list addresses", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + perPage - 1) / perPage

	h.render(w, "addresses.html", map[string]interface{}{
		"Addresses":  addresses,
		"Total":      total,
		"CurPage":    page,
		"TotalPages": totalPages,
		"Search":     search,
		"Network":    network,
		"ShowAll":    showAll,
	})
}

// handleAddressDetail renders the address detail page with transactions.
func (h *Handler) handleAddressDetail(w http.ResponseWriter, r *http.Request) {
	prefix := h.basePath + "/addresses/"
	address := strings.TrimPrefix(r.URL.Path, prefix)
	if address == "" {
		http.Redirect(w, r, h.basePath+"/addresses", http.StatusFound)
		return
	}

	// CSV export: /addresses/{address}/export
	if strings.HasSuffix(address, "/export") {
		h.handleAddressExport(w, r)
		return
	}

	network := r.URL.Query().Get("network")
	if network == "" {
		network = "tron"
	}

	watched, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("admin: get watched", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if watched == nil {
		http.Error(w, "address not found", http.StatusNotFound)
		return
	}

	txPage, _ := strconv.Atoi(r.URL.Query().Get("txpage"))
	if txPage < 1 {
		txPage = 1
	}
	const txPerPage = 25
	txOffset := (txPage - 1) * txPerPage

	txs, txTotal, err := db.GetTransactions(r.Context(), h.db, watched.ID, db.TxFilter{
		Limit:  txPerPage,
		Offset: txOffset,
	})
	if err != nil {
		slog.Error("admin: get transactions", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	txTotalPages := (txTotal + txPerPage - 1) / txPerPage

	h.render(w, "address_detail.html", map[string]interface{}{
		"Watched":      watched,
		"Network":      network,
		"Transactions": txs,
		"TxTotal":      txTotal,
		"TxPage":       txPage,
		"TxTotalPages": txTotalPages,
	})
}

// handleAddressExport streams all transactions of a watched address as CSV.
func (h *Handler) handleAddressExport(w http.ResponseWriter, r *http.Request) {
	prefix := h.basePath + "/addresses/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	address := strings.TrimSuffix(rest, "/export")
	if address == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	network := r.URL.Query().Get("network")
	if network == "" {
		network = "tron"
	}

	watched, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("admin: export get watched", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if watched == nil {
		http.Error(w, "address not found", http.StatusNotFound)
		return
	}

	// Fetch the entire table for this address — no pagination limits.
	txs, _, err := db.GetTransactions(r.Context(), h.db, watched.ID, db.TxFilter{
		All: true,
	})
	if err != nil {
		slog.Error("admin: export get transactions", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	filename := address + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	// UTF-8 BOM so Excel detects encoding correctly.
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}

	cw := csv.NewWriter(w)
	// Column order mirrors TronScan's export for easy comparison.
	_ = cw.Write([]string{"Txn Hash", "Block", "Time(UTC)", "From", "To", "Token", "Token Symbol", "Amount/TokenID", "Result", "Status"})
	for _, t := range txs {
		blockNum := ""
		if t.BlockNum != nil {
			blockNum = strconv.FormatInt(*t.BlockNum, 10)
		}
		_ = cw.Write([]string{
			t.TxID,
			blockNum,
			t.Ts.UTC().Format("2006-01-02 15:04:05"),
			t.FromAddress,
			t.ToAddress,
			"", // Token (contract) — not stored
			t.Currency,
			t.Amount,
			"", // Result — not stored
			"", // Status — not stored
		})
	}
	cw.Flush()
}

// handleAddressAction handles start/stop POST for a watched address.
func (h *Handler) handleAddressAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	network := r.FormValue("network")
	address := r.FormValue("address")
	action := r.FormValue("action")

	if network == "" || address == "" || (action != "start" && action != "stop" && action != "delete") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if action == "delete" {
		if err := db.DeleteWatched(r.Context(), h.db, network, address); err != nil {
			slog.Error("admin: delete address", "err", err)
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, h.basePath+"/addresses", http.StatusSeeOther)
		return
	}

	if err := db.SetWatchedActive(r.Context(), h.db, network, address, action == "start"); err != nil {
		slog.Error("admin: toggle address", "err", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.basePath+"/addresses/"+address+"?network="+network, http.StatusSeeOther)
}

// logEntry holds a single parsed log line.
type logEntry struct {
	Raw   string
	Level string
}

// handleLogs renders the log viewer page.
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// collect available log dates
	availableDates, err := h.availableLogDates()
	if err != nil {
		slog.Warn("admin: list log files", "err", err)
	}

	// selected date (default: today)
	selectedDate := q.Get("date")
	if selectedDate == "" {
		selectedDate = time.Now().Format("2006-01-02")
	}

	tailStr := q.Get("tail")
	tail, _ := strconv.Atoi(tailStr)
	if tail <= 0 {
		tail = 500
	}
	if tail > 10000 {
		tail = 10000
	}

	logPath := filepath.Join(h.logDir, selectedDate+".log")
	lines, fileSize, err := readLogTail(logPath, tail)
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("admin: read log file", "path", logPath, "err", err)
	}

	h.render(w, "logs.html", map[string]interface{}{
		"AvailableDates": availableDates,
		"SelectedDate":   selectedDate,
		"Tail":           tail,
		"Lines":          lines,
		"FileSize":       formatFileSize(fileSize),
		"LogPath":        logPath,
	})
}

func (h *Handler) availableLogDates() ([]string, error) {
	entries, err := os.ReadDir(h.logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dates []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".log") {
			date := strings.TrimSuffix(name, ".log")
			if len(date) == 10 {
				dates = append(dates, date)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates, nil
}

func readLogTail(path string, n int) ([]logEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	info, _ := f.Stat()
	var size int64
	if info != nil {
		size = info.Size()
	}

	var all []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		all = append(all, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, size, err
	}

	start := 0
	if len(all) > n {
		start = len(all) - n
	}
	all = all[start:]

	entries := make([]logEntry, len(all))
	for i, line := range all {
		entries[i] = logEntry{
			Raw:   line,
			Level: extractLevel(line),
		}
	}
	return entries, size, nil
}

func extractLevel(line string) string {
	if idx := strings.Index(line, "level="); idx >= 0 {
		rest := line[idx+6:]
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			end = len(rest)
		}
		return strings.ToUpper(rest[:end])
	}
	return "INFO"
}

func formatFileSize(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
