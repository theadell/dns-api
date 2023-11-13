package main

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/theadell/dnsify/internal/auth"
	"github.com/theadell/dnsify/internal/dnsservice"
)

func (app *App) IndexHandler(w http.ResponseWriter, r *http.Request) {
	errorMsg := app.sessionManager.PopString(r.Context(), auth.LoginErrKey)

	data := LoginTemplateData{
		LoginPromptData: app.idp.LoginPromptData,
		ErrorMessage:    errorMsg,
	}
	app.render(w, http.StatusOK, "index", data)
}
func (app *App) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	records := app.dnsClient.GetRecords()
	records = slices.DeleteFunc(records, func(r dnsservice.Record) bool {
		return r.Type != "A"
	})
	data := DashboardPageData{
		Zone:    app.dnsClient.GetZone(),
		Records: records,
	}
	app.render(w, http.StatusOK, "dashboard", data)
}
func (app *App) SettingsHandler(w http.ResponseWriter, r *http.Request) {
	user := app.sessionManager.GetString(r.Context(), "email")
	keys, _ := app.keyManager.GetKeys(r.Context(), user)
	app.render(w, http.StatusOK, "apikeys", keys)
}

func (app *App) CreateAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	label := r.FormValue("label")
	if len(label) < 4 {
		app.clientError(w, http.StatusBadRequest, "Label Must have at least 4 chars")
		return
	}

	user := app.sessionManager.GetString(r.Context(), "email")
	key, err := app.keyManager.CreateKey(r.Context(), user, label)
	if err != nil {
		app.serverError(w, err)
		return
	}
	app.renderTemplateFragment(w, http.StatusOK, "apikeys", "key-row", key)
}
func (app *App) DeleteAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	keyLabel := chi.URLParam(r, "label")
	if len(keyLabel) < 4 {
		app.clientError(w, http.StatusBadRequest, "Label Must have at least 4 chars")
		return
	}

	user := app.sessionManager.GetString(r.Context(), "email")
	app.keyManager.DeleteKey(r.Context(), user, keyLabel)

	w.WriteHeader(http.StatusOK)
}

func (app *App) configHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.FormValue("hash")
	if hash == "" {
		app.clientError(w, http.StatusBadRequest, "Invalid or empty record id/hash was submitted")
		return
	}
	record := app.dnsClient.GetRecordByHash(hash)
	if record == nil {
		app.clientError(w, http.StatusBadRequest, "No matching record found")
		return
	}
	aaaaRecord := app.dnsClient.GetRecordForFQDN(record.FQDN, "AAAA")
	c := NewNginxConfig(*record, aaaaRecord, "http://localhost:8080")
	app.render(w, http.StatusOK, "nginx-config", c)
}
func (app *App) configAdjusterHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}

	hash := r.FormValue("hash")
	if hash == "" {
		app.clientError(w, http.StatusBadRequest, "Invalid or empty record id/hash was submitted")
		return
	}

	record := app.dnsClient.GetRecordByHash(hash)
	if record == nil {
		app.clientError(w, http.StatusBadRequest, "No matching record found")
		return
	}
	c := app.createNginxConfigFromForm(r, record)
	app.render(w, http.StatusOK, "nginx", c)
}

func (app *App) GetRecordsHandler(w http.ResponseWriter, r *http.Request) {
	recordType := r.URL.Query().Get("type")
	if recordType != "A" && recordType != "AAAA" {
		app.clientError(w, http.StatusBadRequest, "Unsupported record type")
		return
	}
	records := app.dnsClient.GetRecords()
	records = slices.DeleteFunc(records, func(r dnsservice.Record) bool {
		return r.Type != recordType
	})
	app.renderTemplateFragment(w, http.StatusOK, "dashboard", "record-rows", records)
}

func (app *App) AddRecordHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}

	hostname, ip, ttl, recordType := r.FormValue("hostname"), r.FormValue("ip"), r.FormValue("ttl"), r.FormValue("type")

	if err := validateRecordReq(hostname, ip, ttl, recordType); err != nil {
		slog.Error("Invalid record request", "error", err)
		app.clientError(w, http.StatusBadRequest, err.Error())
		return
	}

	ttlNum, err := stringToUint(ttl)
	if err != nil {
		app.clientError(w, http.StatusBadRequest, "Invalid TTL", err.Error())
		return
	}

	record := dnsservice.NewRecordFromHost(recordType, hostname, ip, ttlNum, app.dnsClient.GetZone(), app.dnsClient.GetIPv4(), app.dnsClient.GetIPv6())
	if app.dnsClient.GetRecordByHash(record.Hash) != nil {
		app.clientError(w, http.StatusBadRequest, "Duplication Error", "Record Already Exists")
		return
	}

	err = app.dnsClient.AddRecord(record)
	if err != nil {
		if err == dnsservice.ErrImmutableRecord {
			app.clientError(w, http.StatusBadRequest, "This record is read only")
			return
		} else if err == dnsservice.ErrNotAuthorized {
			app.clientError(w, http.StatusUnauthorized, "You do not have the required permissions to perform this action.")
			return
		}
		app.serverError(w, err)
		return
	}
	app.renderTemplateFragment(w, http.StatusOK, "dashboard", "record-row", &record)
}

func (app *App) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	app.render(w, http.StatusNotFound, "error", nil)
}

func (app *App) DeleteRecordHandler(w http.ResponseWriter, r *http.Request) {

	recordType := strings.TrimSpace(r.URL.Query().Get("Type"))
	fqdn := strings.TrimSpace(r.URL.Query().Get("FQDN"))
	ip := strings.TrimSpace(r.URL.Query().Get("IP"))
	ttl := strings.TrimSpace(r.URL.Query().Get("TTL"))

	if !isValidType(recordType) {
		app.clientError(w, http.StatusBadRequest, "Invalid DNS Record Type")
		return
	}

	if !isValidFQDN(fqdn) || (recordType == "A" && !isValidIPv4(ip) || recordType == "AAAA" && !isValidIPv6(ip)) || !isValidTTL(ttl) {
		app.clientError(w, http.StatusBadRequest, "Invalid record")
		return
	}

	record := dnsservice.Record{
		Type: recordType,
		FQDN: fqdn,
		IP:   ip,
	}

	err := app.dnsClient.RemoveRecord(record)
	if err != nil {
		if err == dnsservice.ErrImmutableRecord {
			app.clientError(w, http.StatusBadRequest, "This record is read only")
			return
		} else if err == dnsservice.ErrNotAuthorized {
			app.clientError(w, http.StatusUnauthorized, "You do not have the required permissions to perform this action.")
			return
		}

		slog.Error("Failed to delete record")
		app.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (app *App) StatusSSEHandler(w http.ResponseWriter, r *http.Request) {
	// Server-Sent Events (SSE) headers as per RFC 8895
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// event id
	id := 0
	rc := http.NewResponseController(w)
	// Override default timeouts for this long-lived connection.
	noDeadline := time.Time{}
	rc.SetReadDeadline(noDeadline)
	rc.SetWriteDeadline(noDeadline)

	ts, ok := app.templateCache["infobar"]
	if !ok {
		slog.Error("couldn't find the `infobar` template")
		return
	}
	sendUpdate := func() {
		b, err := ConstructSSEMessage(ts, app.dnsClient.HealthCheck(), "message", id)
		if err != nil {
			slog.Error("Failed to execute infobar template", "error", err)
			return
		}
		_, err = w.Write(b)
		if err != nil {
			slog.Error("Failed to write bytes to SSE connection", "error", err)
			return
		}

		// flush data immediately to client
		err = rc.Flush()
		if err != nil {
			slog.Error("Failed to flush data to client", "error", err)
			return
		}

		id++ // increment the message id
	}

	// Immediately send the current status when a client connects.
	sendUpdate()

	// Periodically send status updates at regular intervals.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sendUpdate()
		case <-r.Context().Done():
			return
		}

	}
}
