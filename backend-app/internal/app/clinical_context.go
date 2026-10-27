package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type externalScale struct {
	Scale      string `json:"scale"`
	Total      *int   `json:"total"`
	AssessedOn string `json:"assessed_on"`
	Source     string `json:"source"`
	Notes      string `json:"notes"`
}
type clinicalContextData struct {
	MedicalHistory     string          `json:"medical_history"`
	PsychiatricHistory string          `json:"psychiatric_history"`
	Diagnoses          string          `json:"diagnoses"`
	Medications        string          `json:"medications"`
	Confounders        string          `json:"confounders"`
	ExternalScales     []externalScale `json:"external_scales"`
}

func (c *clinicalContextData) validate() bool {
	fields := []*string{&c.MedicalHistory, &c.PsychiatricHistory, &c.Diagnoses, &c.Medications, &c.Confounders}
	for _, f := range fields {
		*f = strings.TrimSpace(*f)
		if !utf8.ValidString(*f) || len([]rune(*f)) > 5000 {
			return false
		}
	}
	if c.Diagnoses == "" {
		return false
	}
	if len(c.ExternalScales) > 30 {
		return false
	}
	if c.ExternalScales == nil {
		c.ExternalScales = []externalScale{}
	}
	maxima := map[string]int{"PHQ9": 27, "GAD7": 21, "MADRS": 60, "HDRS17": 52}
	for i := range c.ExternalScales {
		e := &c.ExternalScales[i]
		max, ok := maxima[e.Scale]
		e.Source = strings.TrimSpace(e.Source)
		e.Notes = strings.TrimSpace(e.Notes)
		if !ok || e.Total == nil || *e.Total < 0 || *e.Total > max || !date(e.AssessedOn) || e.Source == "" || len([]rune(e.Source)) > 1000 || len([]rune(e.Notes)) > 2000 || !utf8.ValidString(e.Source+e.Notes) {
			return false
		}
	}
	return true
}

const clinicalVersionFields = `v.id,v.patient_id,v.version,v.context,v.author_id,u.name author_name,v.created_at`

func (s *Server) clinicalAccess(w http.ResponseWriter, r *http.Request) bool {
	if current(r).Role != "doctor" || !uuid(r.PathValue("id")) {
		writeError(w, 403, "Клинический контекст доступен только лечащему врачу")
		return false
	}
	var allowed bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM care_links c JOIN users p ON p.id=c.patient_id WHERE c.doctor_id=$1 AND c.patient_id=$2 AND c.active AND p.active AND p.role='patient')`, current(r).ID, r.PathValue("id")).Scan(&allowed); err != nil {
		s.fail(w, r, err)
		return false
	}
	if !allowed {
		writeError(w, 403, "Нет действующего доступа к клиническому контексту")
		return false
	}
	return true
}
func lockClinicalAccess(ctx context.Context, tx pgx.Tx, patient, doctor string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT p.id FROM users p JOIN care_links c ON c.patient_id=p.id JOIN users d ON d.id=c.doctor_id WHERE p.id=$1 AND d.id=$2 AND p.active AND p.role='patient' AND c.active AND d.active AND d.role='doctor' FOR UPDATE OF p FOR SHARE OF c,d`, patient, doctor).Scan(&id)
}
func (s *Server) clinicalContext(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalAccess(w, r) {
		return
	}
	data, err := s.jsonQuery(r.Context(), `SELECT coalesce((SELECT to_jsonb(x) FROM (SELECT `+clinicalVersionFields+` FROM clinical_context_versions v JOIN users u ON u.id=v.author_id WHERE v.patient_id=$1 ORDER BY version DESC LIMIT 1)x),'{"version":0,"context":null}'::jsonb)`, r.PathValue("id"))
	// Read and audit use the same authorization boundary; context never joins patient UI payloads.
	if err == nil {
		err = s.recordRead(r, "clinical_context.read", r.PathValue("id"))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) clinicalContextHistory(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalAccess(w, r) {
		return
	}
	if err := s.recordRead(r, "clinical_context.history", r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.listPage(w, r, `SELECT `+clinicalVersionFields+` FROM clinical_context_versions v JOIN users u ON u.id=v.author_id WHERE v.patient_id=$1`, r.PathValue("id"))
}
func (s *Server) saveClinicalContext(w http.ResponseWriter, r *http.Request) {
	if !s.clinicalAccess(w, r) {
		return
	}
	var in struct {
		BaseVersion *int                 `json:"base_version"`
		RequestKey  string               `json:"request_key"`
		Context     *clinicalContextData `json:"context"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.BaseVersion == nil || *in.BaseVersion < 0 || !uuid(in.RequestKey) || in.Context == nil || !in.Context.validate() {
		writeError(w, 400, "Укажите поставленный диагноз и проверьте клинические данные и источник внешней шкалы")
		return
	}
	raw, _ := json.Marshal(in)
	requestHash := hash(string(raw))
	patient, doctor := r.PathValue("id"), current(r).ID
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, r.Context())
	if err = lockClinicalAccess(r.Context(), tx, patient, doctor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 403, "Доступ к пациенту отозван")
		} else {
			s.fail(w, r, err)
		}
		return
	}
	var prior []byte
	var priorHash string
	err = tx.QueryRow(r.Context(), `SELECT to_jsonb(x)-'request_hash',request_hash FROM (SELECT `+clinicalVersionFields+`,v.request_hash FROM clinical_context_versions v JOIN users u ON u.id=v.author_id WHERE v.patient_id=$1 AND v.author_id=$2 AND request_key=$3)x`, patient, doctor, in.RequestKey).Scan(&prior, &priorHash)
	if err == nil {
		if priorHash != requestHash {
			writeError(w, 409, "Ключ уже использован для другой версии")
			return
		}
		writeJSON(w, 200, json.RawMessage(prior))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	var latest int
	var latestDiagnoses string
	if err = tx.QueryRow(r.Context(), `SELECT coalesce(max(version),0),coalesce((SELECT context->>'diagnoses' FROM clinical_context_versions WHERE patient_id=$1 ORDER BY version DESC LIMIT 1),'') FROM clinical_context_versions WHERE patient_id=$1`, patient).Scan(&latest, &latestDiagnoses); err != nil {
		s.fail(w, r, err)
		return
	}
	if latest != *in.BaseVersion {
		writeError(w, 409, "Контекст изменён другим врачом. Загрузите актуальную версию")
		return
	}
	contextJSON, _ := json.Marshal(in.Context)
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO clinical_context_versions(patient_id,version,author_id,context,request_key,request_hash) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, patient, latest+1, doctor, contextJSON, in.RequestKey, requestHash).Scan(&id)
	if err == nil && in.Context.Diagnoses != "" && in.Context.Diagnoses != strings.TrimSpace(latestDiagnoses) {
		_, err = tx.Exec(r.Context(), `INSERT INTO clinical_evidence_reviews(patient_id,context_version_id,doctor_id,diagnoses,diagnosis_hash,prompt_version) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, patient, id, doctor, in.Context.Diagnoses, hash(in.Context.Diagnoses), clinicalEvidencePromptVersion)
	}
	if err == nil {
		err = audit(r.Context(), tx, current(r), "clinical_context.create", id, r.Context().Value(requestKey{}).(string))
	}
	var result []byte
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT to_jsonb(x) FROM (SELECT `+clinicalVersionFields+` FROM clinical_context_versions v JOIN users u ON u.id=v.author_id WHERE v.id=$1)x`, id).Scan(&result)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, json.RawMessage(result))
}
