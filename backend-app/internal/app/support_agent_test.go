package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func configureAgentModel(f *fixture, url string) {
	f.server.config.SupportChatMode = "openai"
	f.server.config.SupportChatURL = url
	f.server.config.SupportChatAPIKey = "agent-test-key"
	f.server.config.SupportChatModel = "test-agent"
}
func agentPost(f *fixture, text, key string) *httptest.ResponseRecorder {
	return f.request("POST", "/patient/support-chat", map[string]any{"text": text, "request_key": key, "external_consent": true}, f.patient)
}
func functionOutput(id, name, args string) map[string]any {
	return map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args}
}
func finalOutput(text string) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
}
func requireAgent(t *testing.T, w *httptest.ResponseRecorder) *supportAgentRun {
	t.Helper()
	requireStatus(t, w, 201)
	run := supportPair(t, w)[1].Agent
	if run == nil {
		t.Fatal("missing durable agent metadata")
	}
	return run
}

func TestIntegrationSupportAgentDemoStateAndRealTasks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `UPDATE assignments SET status='paused' WHERE patient_id=$1`, f.patient.ID); err != nil {
		t.Fatal(err)
	}
	var assignmentID string
	if err := f.db.QueryRow(ctx, `INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by) VALUES($1,'ema',$2::date,$2::date+10,'00:00',$3) RETURNING id`, f.patient.ID, f.today, f.doctor.ID).Scan(&assignmentID); err != nil {
		t.Fatal(err)
	}
	// Same kind but future / wrong interval / paused must never become today's task.
	for _, query := range []string{
		`INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by) VALUES($1,'csd',$2::date+1,$2::date+3,'08:00',$3)`,
		`INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by,interval_days) VALUES($1,'csd',$2::date-1,$2::date+3,'08:00',$3,2)`,
		`INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by,status) VALUES($1,'csd',$2::date,$2::date+3,'08:00',$3,'paused')`,
	} {
		if _, err := f.db.Exec(ctx, query, f.patient.ID, f.today, f.doctor.ID); err != nil {
			t.Fatal(err)
		}
	}
	first := supportPost(f, "Какие задачи сегодня?", "71000000-0000-4000-8000-000000000001", f.patient)
	run := requireAgent(t, first)
	if run.Mode != "demo" || run.State.Focus != "tasks" || run.PreviousStateVersion != 0 || run.State.Version != 1 || run.ModelRounds != 0 || run.ToolCalls != 1 || run.Tools[0].Name != "get_today_tasks" {
		t.Fatalf("invalid first run %+v", run)
	}
	var tasks supportTodayTasks
	if err := json.Unmarshal(run.Tools[0].Result, &tasks); err != nil {
		t.Fatal(err)
	}
	if tasks.Date != f.today || tasks.Timezone != "Europe/Moscow" || len(tasks.Items) != 1 || tasks.Items[0].AssignmentID != assignmentID || tasks.Items[0].Status != "due" || tasks.Items[0].EntryID != nil {
		t.Fatalf("not grounded in today assignments: %+v", tasks)
	}
	if !strings.Contains(supportPair(t, first)[1].Text, "время заполнения наступило") {
		t.Fatal("demo did not consume tool result")
	}
	// Reload handler, complete the actual diary separately, then continue the topic.
	f.server = New(f.db, f.server.config)
	entry := f.request("POST", "/patient/diaries", emaInput(f.today, "71000000-0000-4000-8000-000000000002", false), f.patient)
	requireStatus(t, entry, 201)
	entryID := decodeID(t, entry)
	second := supportPost(f, "помоги с этим", "71000000-0000-4000-8000-000000000003", f.patient)
	continued := requireAgent(t, second)
	if continued.PreviousStateVersion != 1 || continued.State.Version != 2 || continued.State.Focus != "tasks" {
		t.Fatal("lost focus after server reload")
	}
	if err := json.Unmarshal(continued.Tools[0].Result, &tasks); err != nil {
		t.Fatal(err)
	}
	if tasks.Items[0].Status != "completed" || tasks.Items[0].EntryID == nil || *tasks.Items[0].EntryID != entryID {
		t.Fatal("continuation did not observe new persisted completion")
	}
	third := supportPost(f, "Объясни CSD дневник сна", "71000000-0000-4000-8000-000000000004", f.patient)
	csd := requireAgent(t, third)
	if csd.State.Focus != "csd" || csd.State.NextStep != "open_diary" || csd.Tools[0].Name != "explain_diary" {
		t.Fatal("failed explicit focus change")
	}
	f.server = New(f.db, f.server.config)
	fourth := supportPost(f, "Помоги с этим", "71000000-0000-4000-8000-000000000005", f.patient)
	follow := requireAgent(t, fourth)
	if follow.State.Focus != "csd" || follow.PreviousStateVersion != csd.State.Version || !strings.Contains(supportPair(t, fourth)[1].Text, "Дневник сна") {
		t.Fatal("diary followup not grounded in saved focus")
	}
	replay := supportPost(f, "Помоги с этим", "71000000-0000-4000-8000-000000000005", f.patient)
	requireStatus(t, replay, 200)
	if supportPair(t, replay)[1].Agent.State.Version != follow.State.Version {
		t.Fatal("idempotency advanced state")
	}
	// Independent patient has its own memory and no first-patient task sources.
	other := supportPost(f, "помоги с этим", "71000000-0000-4000-8000-000000000006", f.other)
	otherRun := requireAgent(t, other)
	if otherRun.PreviousStateVersion != 0 || strings.Contains(other.Body.String(), assignmentID) {
		t.Fatal("crosspatient agent state leak")
	}
}
func TestIntegrationSupportAgentResponsesToolLoop(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var assignmentID string
	if err := f.db.QueryRow(ctx, `INSERT INTO assignments(patient_id,kind,start_date,end_date,due_time,created_by) VALUES($1,'ema',$2::date,$2::date+1,'00:00',$3) RETURNING id`, f.patient.ID, f.today, f.doctor.ID).Scan(&assignmentID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	const encrypted = "opaque-reasoning-test-never-persist"
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round := calls.Add(1)
		var request struct {
			Input []json.RawMessage `json:"input"`
			Tools []struct {
				Type       string         `json:"type"`
				Name       string         `json:"name"`
				Strict     bool           `json:"strict"`
				Parameters map[string]any `json:"parameters"`
			} `json:"tools"`
			Store    bool     `json:"store"`
			Include  []string `json:"include"`
			Parallel bool     `json:"parallel_tool_calls"`
			Choice   string   `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Store || request.Parallel || len(request.Tools) != 3 || len(request.Include) != 1 || request.Include[0] != "reasoning.encrypted_content" {
			t.Error("unsafe stateless agent request")
		}
		for _, tool := range request.Tools {
			if !tool.Strict || tool.Type != "function" || tool.Parameters["additionalProperties"] != false {
				t.Error("tools not strict")
			}
		}
		if round == 1 {
			if request.Choice != "required" {
				t.Error("first observation not required")
			}
			writeJSON(w, 200, map[string]any{"status": "completed", "output": []any{map[string]any{"type": "reasoning", "id": "rs_test", "summary": []any{}, "encrypted_content": encrypted}, functionOutput("call_tasks", "get_today_tasks", `{}`), finalOutput("Premature text must not be published")}})
		} else {
			raw, _ := json.Marshal(request.Input)
			if !strings.Contains(string(raw), encrypted) || !strings.Contains(string(raw), assignmentID) || !strings.Contains(string(raw), "function_call_output") || !strings.Contains(string(raw), "call_tasks") || !strings.Contains(string(raw), f.today) {
				t.Error("lost opaque reasoning or actual tool observation")
			}
			if strings.Contains(string(raw), "medical_history") {
				t.Error("clinical context exposed")
			}
			writeJSON(w, 200, map[string]any{"status": "completed", "output": []any{finalOutput("На сегодня есть задания; можно открыть раздел «Сегодня».")}})
		}
	}))
	defer model.Close()
	configureAgentModel(f, model.URL)
	response := agentPost(f, "Какие задачи сегодня?", "71000000-0000-4000-8000-000000000007")
	run := requireAgent(t, response)
	if run.Status != "completed" || run.ModelRounds != 2 || run.ToolCalls != 1 || run.State.Focus != "tasks" || calls.Load() != 2 {
		t.Fatalf("not a completed tool loop: %+v", run)
	}
	if strings.Contains(response.Body.String(), encrypted) || strings.Contains(response.Body.String(), "Premature text") {
		t.Fatal("reasoning or premature reply leaked")
	}
	var stored string
	if err := f.db.QueryRow(ctx, `SELECT to_jsonb(t)::text FROM support_chat_turns t WHERE patient_id=$1`, f.patient.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, encrypted) || strings.Contains(stored, "rs_test") {
		t.Fatal("opaque provider output persisted")
	}
}
func TestIntegrationSupportAgentRejectedPlansAndBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		output     func(int) []any
	}{
		{"unknown", "tool_rejected", func(int) []any { return []any{functionOutput("call1", "send_doctor_message", `{}`)} }},
		{"spoofed identity", "tool_rejected", func(int) []any {
			return []any{functionOutput("call1", "get_today_tasks", `{"patient_id":"someone-else"}`)}
		}},
		{"invalid diary", "tool_rejected", func(int) []any {
			return []any{functionOutput("call1", "explain_diary", `{"kind":"ema","field":"bedtime"}`)}
		}},
		{"malformed", "invalid_tool_arguments", func(int) []any { return []any{functionOutput("call1", "get_today_tasks", `{`)} }},
		{"duplicate", "duplicate_tool_call", func(int) []any { return []any{functionOutput("call1", "get_today_tasks", `{}`)} }},
		{"too many", "tool_budget_exceeded", func(int) []any {
			out := []any{}
			for i := 0; i < 5; i++ {
				out = append(out, functionOutput(fmt.Sprintf("call%d", i), "get_today_tasks", `{}`))
			}
			return out
		}},
		{"rounds", "round_budget_exceeded", func(i int) []any {
			return []any{functionOutput(fmt.Sprintf("call%d", i), "propose_support_step", fmt.Sprintf(`{"kind":"%s","diary_kind":null}`, []string{"talk", "small_step", "doctor_message"}[i-1]))}
		}},
		{"missing required tool", "missing_tool_call", func(int) []any { return []any{finalOutput("Pretend complete.")} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			prior := requireAgent(t, supportPost(f, "Объясни EMA", "71000000-0000-4000-8000-000000000008", f.patient))
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				writeJSON(w, 200, map[string]any{"status": "completed", "output": tc.output(n)})
			}))
			defer model.Close()
			configureAgentModel(f, model.URL)
			response := agentPost(f, "Продолжим", "71000000-0000-4000-8000-000000000009")
			run := requireAgent(t, response)
			if run.Status != "blocked" || run.ErrorCode != tc.code || run.State.Focus != prior.State.Focus || run.PreviousStateVersion != prior.State.Version || run.State.Version != prior.State.Version+1 || run.ModelRounds > 3 || run.ToolCalls > 4 {
				t.Fatalf("unsafe plan accepted or memory changed: %+v", run)
			}
			if supportPair(t, response)[1].Safety != "provider_unavailable" {
				t.Fatal("failed agent plan presented as valid model reply")
			}
		})
	}
}
func TestSupportAgentToolArguments(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"get_today_tasks", `{"patient_id":"x"}`}, {"get_today_tasks", `null`}, {"explain_diary", `{"kind":"ema"}`}, {"explain_diary", `{"kind":"ema","field":"bedtime"}`}, {"explain_diary", `{"kind":"phq9","field":null}`}, {"propose_support_step", `{"kind":"send_message","diary_kind":null}`}, {"propose_support_step", `{"kind":"talk","diary_kind":"ema"}`},
	} {
		_, _, err := executeSupportTool(context.Background(), nil, "server-patient", tc.name, json.RawMessage(tc.args), supportAgentState{Focus: "support", NextStep: "none"})
		if err == nil {
			t.Fatalf("accepted invalid %s args", tc.name)
		}
	}
}

func TestIntegrationSupportAgentRevocationStopsContinuation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round := calls.Add(1)
		if round != 1 {
			t.Error("external continuation received after session revocation")
		}
		// Revoke during the already in-flight planning call. Its tool request must not
		// be dispatched, and no tool observations may be sent in another model round.
		if _, err := f.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hash(f.patient.Session)); err != nil {
			t.Error(err)
		}
		writeJSON(w, 200, map[string]any{"status": "completed", "output": []any{functionOutput("call_revoked", "get_today_tasks", `{}`)}})
	}))
	defer model.Close()
	configureAgentModel(f, model.URL)
	response := agentPost(f, "Какие задачи сегодня?", "71000000-0000-4000-8000-000000000010")
	requireStatus(t, response, 401)
	if calls.Load() != 1 {
		t.Fatal("agent continued after revocation")
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM support_chat_turns WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked session published agent metadata or messages")
	}
}

func TestIntegrationSupportAgentExpiryStopsContinuation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Expiry occurs after the agent transaction began: transaction-stable now()
		// would miss it, while the dispatch and publication guards must use real time.
		if _, err := f.db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 microsecond' WHERE token_hash=$1`, hash(f.patient.Session)); err != nil {
			t.Error(err)
		}
		writeJSON(w, 200, map[string]any{"status": "completed", "output": []any{functionOutput("call_expired", "get_today_tasks", `{}`)}})
	}))
	defer model.Close()
	configureAgentModel(f, model.URL)
	requireStatus(t, agentPost(f, "Какие задачи сегодня?", "71000000-0000-4000-8000-000000000011"), 401)
	if calls.Load() != 1 {
		t.Fatal("agent continued after session expiry")
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM support_chat_turns WHERE patient_id=$1`, f.patient.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired session published a turn")
	}
}
