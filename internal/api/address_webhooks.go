package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/db"
)

// GET /api/addresses/{address}/webhooks?network=...
func (h *Handler) handleListAddressWebhooks(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	network := r.URL.Query().Get("network")

	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	wa, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("list address webhooks: get address", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if wa == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}

	items := make([]map[string]interface{}, 0, len(wa.Webhooks))
	for _, wh := range wa.Webhooks {
		items = append(items, webhookToMap(wh))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"network":  wa.Network,
		"address":  wa.Address,
		"webhooks": items,
	})
}

// POST /api/addresses/{address}/webhooks?network=...
//
// Body: {"url":"...","secret":"...","timeout_sec":10,"max_retries":3}
func (h *Handler) handleAddAddressWebhook(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	network := r.URL.Query().Get("network")

	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	var req struct {
		URL        string `json:"url"`
		Secret     string `json:"secret"`
		TimeoutSec int    `json:"timeout_sec"`
		MaxRetries int    `json:"max_retries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	wa, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("add address webhook: get address", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if wa == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}

	wh, err := db.AddAddressWebhook(r.Context(), h.db, wa.ID, db.WebhookRequest{
		URL:        req.URL,
		Secret:     req.Secret,
		TimeoutSec: req.TimeoutSec,
		MaxRetries: req.MaxRetries,
	})
	if err != nil {
		slog.Error("add address webhook", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusCreated, webhookToMap(*wh))
}

// DELETE /api/addresses/{address}/webhooks/{id}?network=...
func (h *Handler) handleDeleteAddressWebhook(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	network := r.URL.Query().Get("network")
	idStr := r.PathValue("id")

	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	webhookID, err := strconv.Atoi(idStr)
	if err != nil || webhookID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid webhook id"})
		return
	}

	wa, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("delete address webhook: get address", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if wa == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}

	if err := db.DeleteAddressWebhook(r.Context(), h.db, webhookID, wa.ID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook not found"})
			return
		}
		slog.Error("delete address webhook", "address", address, "webhook_id", webhookID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":     webhookID,
		"status": "deleted",
	})
}

func webhookToMap(wh db.AddressWebhook) map[string]interface{} {
	return map[string]interface{}{
		"id":          wh.ID,
		"url":         wh.URL,
		"timeout_sec": wh.TimeoutSec,
		"max_retries": wh.MaxRetries,
		"active":      wh.Active,
		"created_at":  wh.CreatedAt.UTC().Format(time.RFC3339),
	}
}
