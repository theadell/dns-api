package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/theadell/dns-api/internal/dnsclient"
)

var excludedFQDNs = map[string]struct{}{
	"rusty-leipzig.com.":     {},
	"ns1.rusty-leipzig.com.": {},
	"ns2.rusty-leipzig.com.": {},
	"www.rusty-leipzig.com.": {},
	"dns.rusty-leipzig.com.": {},
}

func (app *App) IndexHandler(w http.ResponseWriter, r *http.Request) {
	app.render(w, http.StatusOK, "index", nil)
}
func (app *App) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	allRecords := app.bindClient.GetRecords()

	var filteredRecords []dnsclient.Record
	for _, record := range allRecords {
		if _, excluded := excludedFQDNs[record.FQDN]; !excluded {
			filteredRecords = append(filteredRecords, record)
		}
	}
	app.render(w, http.StatusOK, "dashboard", filteredRecords)
}

func (app *App) configHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.FormValue("hash")
	if hash == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Empty Hash\n"))
		return
	}
	record := app.bindClient.GetRecordByHash(hash)
	if record == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No matching record StatusNotFound\n"))
		return
	}
	aaaaRecord := app.bindClient.GetRecordForFQDN(record.FQDN, "AAAA")
	c := NewNginxConfig(*record, aaaaRecord, "http://localhost:8080")
	app.render(w, http.StatusOK, "nginx-config", c)
}

func (app *App) configAdjusterHandler(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	hash := r.FormValue("hash")
	if hash == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Empty Hash\n"))
		return
	}
	record := app.bindClient.GetRecordByHash(hash)
	if record == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No matching record StatusNotFound\n"))
		return
	}
	aaaaRecord := app.bindClient.GetRecordForFQDN(record.FQDN, "AAAA")
	listenAddress := r.FormValue("listen_address")
	useHttp2 := r.FormValue("use_http2") == "on"
	wsHeaders := r.FormValue("ws_headers") == "on"
	cloudflareResolver := r.FormValue("cloudflare_resolver") == "on"
	googlePublicDNS := r.FormValue("google_public_dns") == "on"
	enableLogging := r.FormValue("enable_logging") == "on"
	enableRateLimiting := r.FormValue("enable_rate_limiting") == "on"
	strictTransport := r.FormValue("strict_transport") == "on"
	includeSubdomains := r.FormValue("include_subdomains") == "on"

	c := NewNginxConfig(*record, aaaaRecord, listenAddress)
	c.UseGooglePublicDNS = googlePublicDNS
	c.UseCloudflareResolver = cloudflareResolver
	c.EnableHSTS = strictTransport
	c.IncludeSubDomains = includeSubdomains
	c.EnableLogging = enableLogging
	c.EnableRateLimit = enableRateLimiting
	c.UseHttp2 = useHttp2
	c.AddWsHeaders = wsHeaders
	app.render(w, http.StatusOK, "nginx-config", c)
}
func (app *App) AddRecordHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		slog.Error("Failed to parse form", "error", err)
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	hostname, ip, ttl, recordType := r.FormValue("hostname"), r.FormValue("ip"), r.FormValue("ttl"), r.FormValue("type")

	if ip == "@" {
		ip = "157.230.106.145"
	}

	if err := validateRecordReq(hostname, ip, ttl, recordType); err != nil {
		slog.Error("Invalid record request", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ttlNum, err := stringToUint(ttl)
	if err != nil {
		slog.Error("Invalid TTL", "ttl", ttl)
		http.Error(w, "Invalid ttl", http.StatusBadRequest)
		return
	}

	record := dnsclient.NewRecord(recordType, fmt.Sprintf("%s.%s.", hostname, "rusty-leipzig.com"), ip, ttlNum)

	if _, excluded := excludedFQDNs[record.FQDN]; excluded {
		http.Error(w, "Addition of this record is not allowed", http.StatusBadRequest)
		return
	}

	if app.bindClient.GetRecordByHash(record.Hash) != nil {
		http.Error(w, "Duplication Error: Record Already exists", http.StatusBadRequest)
		return
	}

	// update existing record
	if dr := app.bindClient.GetRecordByFQDNAndType(record.FQDN, record.Type); dr != nil {
		if app.bindClient.RemoveRecord(*dr) != nil {
			slog.Error("Failed to remove record", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("Successfully deleted DNS record.", "record", record)
		err = app.bindClient.AddRecord(record)
		if err != nil {
			slog.Error("Failed to add record", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		slog.Info("Successfully added a new DNS record.", "record", record)
		payload := HTMXDeleteDuplicateRowEvent{}
		payload.DeleteDuplicateRow.Hash = dr.Hash
		jsonHeader, err := json.Marshal(payload)
		if err != nil {
			slog.Error("Failed to create header", "error", err)
			http.Error(w, "Failed to create header", http.StatusInternalServerError)
			return
		}
		// instruct htmx to remove the old record
		w.Header().Set("HX-Trigger", string(jsonHeader))
		app.render(w, http.StatusOK, "record_fragment", record)
		return
	}

	err = app.bindClient.AddRecord(record)
	if err != nil {
		slog.Error("Failed to add record", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("Successfully added a new DNS record.", "record", record)
	app.render(w, http.StatusOK, "record_fragment", &record)
}

func (app *App) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	tmpl, _ := app.templateCache["error"]
	tmpl.Execute(w, nil)
}

func (app *App) DeleteRecordHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	recordType := strings.TrimSpace(r.URL.Query().Get("Type"))
	fqdn := strings.TrimSpace(r.URL.Query().Get("FQDN"))
	ip := strings.TrimSpace(r.URL.Query().Get("IP"))
	ttl := strings.TrimSpace(r.URL.Query().Get("TTL"))

	if !isValidType(recordType) {
		http.Error(w, "Invalid record type", http.StatusBadRequest)
		return
	}

	if !isValidFQDN(fqdn) || (recordType == "A" && !isValidIPv4(ip) || recordType == "AAAA" && !isValidIPv6(ip)) || !isValidTTL(ttl) {
		http.Error(w, "Invalid input values", http.StatusBadRequest)
		return
	}

	record := dnsclient.Record{
		Type: recordType,
		FQDN: fqdn,
		IP:   ip,
	}

	if _, excluded := excludedFQDNs[record.FQDN]; excluded {
		http.Error(w, "Deletion of this record is not allowed", http.StatusBadRequest)
		return
	}
	err := app.bindClient.RemoveRecord(record)
	if err != nil {
		slog.Error("Failed to delete record", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("Successfully deleted a dns record.", "record", record)
	w.WriteHeader(http.StatusOK)
}
