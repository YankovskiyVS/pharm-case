package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const supportAgentVersion = "support-agent-v1"
const supportMaxRounds = 3
const supportMaxTools = 4

type supportAgentState struct {
	Version  int    `json:"version"`
	Focus    string `json:"focus"`
	NextStep string `json:"next_step"`
}
type supportAgentTool struct {
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Result  json.RawMessage `json:"result,omitempty"`
}
type supportAgentRun struct {
	Version              string             `json:"version"`
	Mode                 string             `json:"mode"`
	Status               string             `json:"status"`
	PreviousStateVersion int                `json:"previous_state_version"`
	State                supportAgentState  `json:"state"`
	Tools                []supportAgentTool `json:"tools"`
	ModelRounds          int                `json:"model_rounds"`
	ToolCalls            int                `json:"tool_calls"`
	MaxModelRounds       int                `json:"max_model_rounds"`
	MaxToolCalls         int                `json:"max_tool_calls"`
	ErrorCode            string             `json:"error_code,omitempty"`
}
type supportAgentCapabilities struct {
	Version        string `json:"version"`
	Enabled        bool   `json:"enabled"`
	MaxModelRounds int    `json:"max_model_rounds"`
	MaxToolCalls   int    `json:"max_tool_calls"`
}

func newSupportAgentRun(mode string, state supportAgentState) supportAgentRun {
	return supportAgentRun{Version: supportAgentVersion, Mode: mode, Status: "completed", PreviousStateVersion: state.Version, State: supportAgentState{Version: state.Version + 1, Focus: state.Focus, NextStep: state.NextStep}, Tools: []supportAgentTool{}, MaxModelRounds: supportMaxRounds, MaxToolCalls: supportMaxTools}
}
func loadSupportAgentState(ctx context.Context, tx pgx.Tx, patient string) (supportAgentState, error) {
	state := supportAgentState{Focus: "support", NextStep: "none"}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT agent->'state' FROM support_chat_turns WHERE patient_id=$1 AND agent IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1`, patient).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if json.Unmarshal(raw, &state) != nil || state.Version < 1 || !supportContainsEnum(state.Focus, "support", "ema", "csd", "tasks", "doctor") || !supportContainsEnum(state.NextStep, "none", "talk", "small_step", "doctor_message", "open_diary") {
		return supportAgentState{}, errors.New("invalid stored support state")
	}
	return state, nil
}
func supportContainsEnum(value string, allowed ...string) bool {
	for _, v := range allowed {
		if value == v {
			return true
		}
	}
	return false
}
func supportAgentFailure(run supportAgentRun, previous supportAgentState, code string) supportAgentRun {
	run.Status = "blocked"
	run.ErrorCode = code
	// Rejected plans cannot change the next-turn memory, even after an earlier read tool.
	run.State = supportAgentState{Version: previous.Version + 1, Focus: previous.Focus, NextStep: previous.NextStep}
	return run
}
func (s *Server) runDemoSupportAgent(ctx context.Context, tx pgx.Tx, patient, text string, run supportAgentRun) (string, supportAgentRun, error) {
	lower := strings.ToLower(text)
	var name, args string
	switch {
	case supportContains(lower, "напомни", "задач", "задани", "запланир", "расписан", "что заполнить", "что сегодня"):
		name = "get_today_tasks"
		args = `{}`
	case supportContains(lower, "ema", "ема", "настроени", "ангедон", "энерги"):
		name = "explain_diary"
		args = `{"kind":"ema","field":null}`
	case supportContains(lower, "csd", "дневник сна", "сна", "сплю", "спал", "сон"):
		name = "explain_diary"
		args = `{"kind":"csd","field":null}`
	case supportContains(lower, "помоги с этим", "продолж", "дальше", "а как", "давай") && run.State.Focus == "tasks":
		name = "get_today_tasks"
		args = `{}`
	case supportContains(lower, "помоги с этим", "продолж", "дальше", "а как", "давай") && supportContainsEnum(run.State.Focus, "ema", "csd"):
		name = "explain_diary"
		args = `{"kind":"` + run.State.Focus + `","field":null}`
	case supportContains(lower, "врачу", "врачом") || run.State.Focus == "doctor" && supportContains(lower, "помоги с этим", "продолж"):
		name = "propose_support_step"
		args = `{"kind":"doctor_message","diary_kind":null}`
	case supportContains(lower, "тревог", "страш", "паник", "плохо", "тяжело"):
		name = "propose_support_step"
		args = `{"kind":"small_step","diary_kind":null}`
	default:
		name = "propose_support_step"
		args = `{"kind":"talk","diary_kind":null}`
	}
	observation, next, err := executeSupportTool(ctx, tx, patient, name, json.RawMessage(args), run.State)
	run.ToolCalls = 1
	if err != nil {
		return "", run, err
	}
	run.Tools = append(run.Tools, observation)
	run.State = next
	var result struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(observation.Result, &result) != nil || result.Text == "" {
		return "", run, errors.New("tool observation missing supportive text")
	}
	return result.Text, run, nil
}
