package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func correctionInput(day, form, supersedes string, mood int) map[string]any {
	return map[string]any{
		"kind":          "ema",
		"local_date":    day,
		"timezone":      "Europe/Moscow",
		"form_id":       form,
		"supersedes_id": supersedes,
		"answers": map[string]any{
			"mood": mood, "anhedonia": 3, "energy": 7, "anxiety": 2,
			"suicidal": false, "comment": "Synthetic deterioration fixture",
		},
	}
}

func TestDeteriorationAlertUsesCompleteVersionedWindow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().In(zone).AddDate(0, 0, -1)
	dates := []string{end.AddDate(0, 0, -2).Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02"), end.Format("2006-01-02")}
	moods := []int{2, 4, 5}
	currentIDs := make([]string, 3)
	for i, day := range dates {
		if err = f.db.QueryRow(ctx, `SELECT id::text FROM current_entries WHERE patient_id=$1 AND kind='ema' AND local_date=$2::date`, f.patient.ID, day).Scan(&currentIDs[i]); err != nil {
			t.Fatal(err)
		}
		form := fmt.Sprintf("81000000-0000-4000-8000-%012d", i+1)
		response := f.request("POST", "/patient/diaries", correctionInput(day, form, currentIDs[i], moods[i]), f.patient)
		requireStatus(t, response, 201)
		currentIDs[i] = decodeID(t, response)
	}
	var alertID, version, start, finish string
	var details []byte
	var sources []string
	err = f.db.QueryRow(ctx, `SELECT id::text,rule_version,period_start::text,period_end::text,details,source_entry_ids::text[] FROM alerts WHERE patient_id=$1 AND type='DETERIORATION'`, f.patient.ID).Scan(&alertID, &version, &start, &finish, &details, &sources)
	if err != nil {
		t.Fatal(err)
	}
	if version != deteriorationRuleVersion || start != dates[0] || finish != dates[2] || len(sources) != 3 {
		t.Fatalf("unexpected provenance: %s %s %s %#v", version, start, finish, sources)
	}
	var indicator map[string]int
	if err = json.Unmarshal(details, &indicator); err != nil {
		t.Fatal(err)
	}
	if indicator["baseline"] != 2 || indicator["current"] != 5 || indicator["delta"] != 3 || indicator["threshold"] != 3 {
		t.Fatalf("unexpected indicator details: %#v", indicator)
	}
	if err = f.server.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	var notifications int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1 AND patient_id=$2 AND type='deterioration.raised'`, f.doctor.ID, f.patient.ID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("deterioration notification count %d: %v", notifications, err)
	}
	// A later correction may invalidate the current indicator but never erases or
	// duplicates the historical alert for the same versioned window.
	response := f.request("POST", "/patient/diaries", correctionInput(dates[2], "81000000-0000-4000-8000-000000000004", currentIDs[2], 1), f.patient)
	requireStatus(t, response, 201)
	var alerts int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE patient_id=$1 AND type='DETERIORATION'`, f.patient.ID).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("historical alert changed: %d %v", alerts, err)
	}
	if alertID == "" {
		t.Fatal("empty alert id")
	}
}

func TestDiaryCSVContainsOnlyCurrentDailyForms(t *testing.T) {
	f := setup(t)
	response := f.request("GET", "/patients/"+f.patient.ID+"/export?format=csv", nil, f.doctor)
	requireStatus(t, response, 200)
	if got := response.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content type %q", got)
	}
	body := bytes.TrimPrefix(response.Body.Bytes(), []byte("\xef\xbb\xbf"))
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 29 { // header plus fourteen EMA and fourteen CSD forms
		t.Fatalf("unexpected CSV row count %d", len(records))
	}
	if strings.Join(records[0], ",") != strings.Join([]string{"patient_id", "local_date", "form_type", "timezone", "form_id", "submitted_at", "mood", "anhedonia", "energy", "anxiety", "suicidal", "comment", "bedtime", "sleep_attempt", "latency_minutes", "awakenings", "awake_minutes", "final_wake", "after_wake_minutes", "early_wake", "early_minutes", "rise_time", "total_sleep_minutes", "quality", "rested", "nap_minutes", "alcohol", "caffeine", "time_in_bed_minutes", "sleep_efficiency", "sleep_midpoint", "waso_minutes", "scoring_version"}, ",") {
		t.Fatalf("unexpected CSV header: %#v", records[0])
	}
	for _, row := range records[1:] {
		if row[0] != f.patient.ID || (row[2] != "ema" && row[2] != "csd") {
			t.Fatalf("unexpected exported row: %#v", row)
		}
	}
	if strings.Contains(string(body), "alert_actions") || strings.Contains(string(body), "clinical_audio_reports") {
		t.Fatal("non-diary sections leaked into daily-form CSV")
	}
	requireStatus(t, f.request("GET", "/patients/"+f.patient.ID+"/export?format=csv", nil, f.other), 403)
}
