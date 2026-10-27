package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const supportDisclaimer = "Бот поддерживает и объясняет работу дневников. Ответы не являются медицинским заключением. Бот не ставит диагнозы и не назначает лечение. Переписка с ботом не заменяет обращение к врачу и не отправляется врачу автоматически."
const supportCrisisReply = "Мне жаль, что вам сейчас так тяжело. Если есть опасность причинить себе вред или вы уже пострадали, обратитесь в местную экстренную службу сейчас. По возможности попросите человека, которому доверяете, побыть рядом. Свяжитесь с лечащим врачом; обычный чат не гарантирует немедленного ответа. Это сообщение боту само по себе не уведомляет врача."
const supportMedicalReply = "Понимаю, что хочется получить ясный ответ. Я не могу определять диагноз, подбирать препараты, дозировку или менять лечение. Обсудите этот вопрос с лечащим врачом — можно записать, что именно вас беспокоит, и отправить ему сообщение в разделе «Переписка». Я могу помочь сформулировать вопрос без медицинских назначений."
const supportUnavailableReply = "Сейчас языковая модель недоступна, поэтому ответ по вашему сообщению не сформирован. Сообщение сохранено. Можно попробовать позже новым сообщением или написать врачу в разделе «Переписка». Если вам нужна срочная помощь, обратитесь к людям рядом и в местную экстренную службу."

type supportProvider struct {
	Mode       string                   `json:"mode"`
	Model      string                   `json:"model"`
	Disclaimer string                   `json:"disclaimer"`
	Ready      bool                     `json:"ready"`
	External   bool                     `json:"external"`
	Agent      supportAgentCapabilities `json:"agent"`
}
type supportMessage struct {
	ID        string           `json:"id"`
	Role      string           `json:"role"`
	Text      string           `json:"text"`
	CreatedAt time.Time        `json:"created_at"`
	Provider  string           `json:"provider"`
	Model     string           `json:"model"`
	Safety    string           `json:"safety"`
	Agent     *supportAgentRun `json:"agent,omitempty"`
}
type supportTurn struct {
	UserID, AssistantID, UserText, AssistantText, Provider, Model, Safety, RequestHash string
	CreatedAt                                                                          time.Time
	Agent                                                                              *supportAgentRun
}

func (t supportTurn) messages() []supportMessage {
	return []supportMessage{
		{ID: t.UserID, Role: "user", Text: t.UserText, CreatedAt: t.CreatedAt, Provider: t.Provider, Model: t.Model, Safety: t.Safety, Agent: t.Agent},
		{ID: t.AssistantID, Role: "assistant", Text: t.AssistantText, CreatedAt: t.CreatedAt.Add(time.Microsecond), Provider: t.Provider, Model: t.Model, Safety: t.Safety, Agent: t.Agent},
	}
}
func (s *Server) supportProvider() supportProvider {
	mode := s.config.SupportChatMode
	if mode == "" {
		mode = "demo"
	}
	model := s.config.SupportChatModel
	if model == "" {
		model = "gpt-5-mini"
	}
	p := supportProvider{Mode: mode, Model: model, Disclaimer: supportDisclaimer, External: mode == "openai", Agent: supportAgentCapabilities{Version: supportAgentVersion, Enabled: mode == "demo" || mode == "openai", MaxModelRounds: supportMaxRounds, MaxToolCalls: supportMaxTools}}
	switch mode {
	case "demo":
		p.Ready = true
		p.Model = "demo-agent-planner-v1"
		p.Disclaimer = "Демонстрационный агент: планировщик работает по правилам, использует инструменты приложения и сохраняет выбранную тему. Языковая модель не подключена. " + supportDisclaimer
	case "openai":
		p.Ready = strings.TrimSpace(s.config.SupportChatAPIKey) != ""
		p.Disclaimer = "Ответы языковой модели могут содержать ошибки. При отправке ваша переписка с ботом и выбранные агентом сведения о заданиях и их заполнении передаются внешнему провайдеру OpenAI. " + supportDisclaimer
	case "ollama":
		p.Ready = false
		p.Disclaimer = "Агентный протокол для Ollama пока не поддерживается; модель не вызывается. " + supportDisclaimer
	case "disabled":
		p.Ready = false
		p.Disclaimer = "Бот отключён. " + supportDisclaimer
	}
	return p
}

const supportHistorySQL = `SELECT m.id,m.role,m.text,m.created_at,t.provider,t.model,t.safety,t.agent FROM support_chat_turns t CROSS JOIN LATERAL (VALUES(t.user_message_id,'user',t.user_text,t.created_at),(t.assistant_message_id,'assistant',t.assistant_text,t.created_at+interval '1 microsecond')) m(id,role,text,created_at) WHERE t.patient_id=$1`

func (s *Server) supportChatHistory(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := pagination(r)
	if err != nil {
		writeError(w, 400, "Проверьте размер страницы и курсор")
		return
	}
	result, err := s.pageQuery(r.Context(), supportHistorySQL, []any{current(r).ID}, limit, cursor)
	if err == nil {
		err = s.recordRead(r, "support-chat.read", current(r).ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": result.Items, "total": result.Total, "next_cursor": result.NextCursor, "provider": s.supportProvider()})
}
func readSupportTurn(ctx context.Context, tx pgx.Tx, patient, key string) (supportTurn, error) {
	var t supportTurn
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT user_message_id,assistant_message_id,user_text,assistant_text,provider,model,safety,request_hash,created_at,agent FROM support_chat_turns WHERE patient_id=$1 AND request_key=$2`, patient, key).Scan(&t.UserID, &t.AssistantID, &t.UserText, &t.AssistantText, &t.Provider, &t.Model, &t.Safety, &t.RequestHash, &t.CreatedAt, &raw)
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &t.Agent)
	}
	return t, err
}
func (s *Server) sendSupportChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text            string `json:"text"`
		Key             string `json:"request_key"`
		ExternalConsent bool   `json:"external_consent"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if !uuid(in.Key) || len([]rune(in.Text)) < 1 || len([]rune(in.Text)) > 2000 {
		writeError(w, 400, "Введите сообщение до 2000 символов и корректный ключ запроса")
		return
	}
	provider := s.supportProvider()
	if provider.Mode == "disabled" {
		writeError(w, 503, "Бот отключён. Можно написать лечащему врачу")
		return
	}
	// Never hold a database connection while waiting for a request slot.
	select {
	case s.supportSlots <- struct{}{}:
		defer func() { <-s.supportSlots }()
	default:
		writeError(w, 429, "Бот занят. Попробуйте немного позже")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rollback(tx, ctx)
	patient := current(r).ID
	var locked bool
	err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('support-chat:'||$1,7310628))`, patient).Scan(&locked)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !locked {
		writeError(w, 409, "Предыдущее сообщение ещё обрабатывается. Дождитесь ответа")
		return
	}
	digest := hash(in.Text + fmt.Sprint(in.ExternalConsent))
	previous, err := readSupportTurn(ctx, tx, patient, in.Key)
	if err == nil {
		if previous.RequestHash != digest {
			writeError(w, 409, "Ключ запроса уже использован для другого сообщения")
			return
		}
		writeJSON(w, 200, map[string]any{"items": previous.messages(), "provider": provider})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	// Safety is determined before any model calls or consent errors.
	reply, safety := supportSafety(in.Text)
	previousState, stateErr := loadSupportAgentState(ctx, tx, patient)
	if stateErr != nil {
		s.fail(w, r, stateErr)
		return
	}
	run := newSupportAgentRun(provider.Mode, previousState)
	actualProvider, actualModel := provider.Mode, provider.Model
	if reply != "" {
		actualProvider = "safety"
		actualModel = "support-safety-v1"
		run.Mode = "safety"
		run.Status = "blocked"
		run.ErrorCode = "safety_guard"
		run.State.Focus = "doctor"
		run.State.NextStep = "doctor_message"
	} else {
		if provider.External && !in.ExternalConsent {
			writeError(w, 400, "Для внешней модели требуется явное согласие на передачу переписки и выбранных сведений о заданиях")
			return
		}
		if provider.Mode == "demo" {
			reply, run, err = s.runDemoSupportAgent(ctx, tx, patient, in.Text, run)
		} else {
			reply, run, err = s.runModelSupportAgent(ctx, tx, patient, in.Text, current(r).Session, provider, run)
		}
		if err != nil {
			code := run.ErrorCode
			if code == "" {
				code = "agent_error"
			}
			run = supportAgentFailure(run, previousState, code)
			if code == "provider_error" || code == "agent_provider_unsupported" || code == "agent_error" {
				run.Status = "unavailable"
			}
			reply = supportUnavailableReply
			safety = "provider_unavailable"
			actualProvider = "unavailable"
			provider.Ready = false
		} else if supportUnsafeOutput(reply) {
			reply = supportMedicalReply
			safety = "medical_boundary"
			actualProvider = "safety"
			actualModel = "support-safety-v1"
			run = supportAgentFailure(run, previousState, "output_guard")
		}
	}
	if safety == "" {
		safety = "standard"
	}

	// Recheck the exact session after the model call; revocation cannot race publication.
	var active string
	err = tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN sessions ss ON ss.user_id=u.id WHERE u.id=$1 AND u.active AND u.role='patient' AND ss.token_hash=$2 AND ss.expires_at>clock_timestamp() AND ss.auth_version=u.auth_version AND ss.auth_role=u.role FOR SHARE OF u,ss`, patient, current(r).Session).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 401, "Сессия завершена. Войдите снова")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var turn supportTurn
	err = tx.QueryRow(ctx, `INSERT INTO support_chat_turns(patient_id,request_key,request_hash,user_text,assistant_text,provider,model,safety,external_consent,agent) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING user_message_id,assistant_message_id,user_text,assistant_text,provider,model,safety,request_hash,created_at`, patient, in.Key, digest, in.Text, reply, actualProvider, actualModel, safety, provider.External && in.ExternalConsent, run).Scan(&turn.UserID, &turn.AssistantID, &turn.UserText, &turn.AssistantText, &turn.Provider, &turn.Model, &turn.Safety, &turn.RequestHash, &turn.CreatedAt)
	turn.Agent = &run
	if err == nil {
		err = audit(ctx, tx, current(r), "support-chat.send", patient, r.Context().Value(requestKey{}).(string))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"items": turn.messages(), "provider": provider})
}
func supportContains(text string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}
func supportSafety(text string) (string, string) {
	text = strings.ToLower(text)
	if supportContains(text, "суицид", "самоуб", "убить себя", "покончить с собой", "не хочу жить", "не хочется жить", "навредить себе", "вред себе", "порезать себя", "умереть", "suicid", "kill myself", "end my life", "self-harm", "self harm", "overdose", "передоз", "порезал", "порезала") {
		return supportCrisisReply, "crisis"
	}
	if supportContains(text, "диагноз", "диагност", "таблет", "препарат", "лекарств", "дозиров", "дозу", "дозы", "антидепресс", "назнач", "принимать", "отменить", "повысить доз", "снизить доз", "мг ", "diagnos", "medication", "prescri", "dosage", "medicine") {
		return supportMedicalReply, "medical_boundary"
	}
	return "", ""
}
func supportDemoReply(text string) string {
	text = strings.ToLower(text)
	switch {
	case supportContains(text, "ema", "ема", "настроени", "ангедон", "энерги"):
		return "EMA — короткий дневник самочувствия: настроение, интерес к делам, энергия, тревога и вопрос о мыслях о самоубийстве. Ориентируйтесь на подписи в форме и отмечайте, как вы себя чувствуете, без попытки получить «правильный» балл. Если вопрос непонятен, можно уточнить его у врача. Что именно вызывает затруднение?"
	case supportContains(text, "csd", "сд", "сна", "спал", "сплю", "сон"):
		return "Дневник сна помогает записать, как прошла ночь: когда легли, пытались уснуть, просыпались и встали. Указывайте время так, как помните; не нужно специально менять сон ради заполнения. Если не уверены в ответе, используйте допустимые пустые поля и комментарий. С каким вопросом дневника помочь?"
	case supportContains(text, "тревог", "страш", "паник", "anx"):
		return "Похоже, сейчас непросто. Если вам подходит, можно на минуту остановиться, удобно сесть и обратить внимание на окружающие предметы. Не обязательно решать всё сразу. Хотите рассказать, что сейчас тревожит больше всего, или сформулировать сообщение врачу?"
	case supportContains(text, "груст", "тяжело", "одинок", "плохо"):
		return "Мне жаль, что вам сейчас тяжело. Спасибо, что об этом написали. Можно начать с одного небольшого посильного шага: рассказать человеку, которому доверяете, как вы себя чувствуете, или написать врачу. Хотите описать, что произошло сегодня?"
	default:
		return "Я могу поддержать разговор, объяснить вопросы дневников сна и настроения или напомнить, где посмотреть задания врача. В этом демонстрационном режиме ответы заранее подготовлены. Что сейчас было бы полезнее: поговорить о самочувствии или разобраться с дневником?"
	}
}
