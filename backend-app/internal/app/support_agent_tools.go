package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
)

type supportTodayTask struct {
	AssignmentID string  `json:"assignment_id"`
	Kind         string  `json:"kind"`
	DueTime      string  `json:"due_time"`
	Status       string  `json:"status"`
	EntryID      *string `json:"entry_id"`
	Available    bool    `json:"available"`
}
type supportTodayTasks struct {
	Date      string             `json:"date"`
	Timezone  string             `json:"timezone"`
	Items     []supportTodayTask `json:"items"`
	Truncated bool               `json:"truncated"`
	Text      string             `json:"text"`
}

func decodeSupportToolArgs(raw json.RawMessage, target any, keys ...string) error {
	if len(raw) > 2048 {
		return errors.New("invalid tool arguments")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil || len(object) != len(keys) {
		return errors.New("invalid tool argument keys")
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return errors.New("missing tool argument")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid tool argument type")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid trailing tool arguments")
	}
	return nil
}
func supportToolResult(name, summary string, value any) (supportAgentTool, error) {
	raw, err := json.Marshal(value)
	return supportAgentTool{Name: name, Status: "completed", Summary: summary, Result: raw}, err
}
func executeSupportTool(ctx context.Context, tx pgx.Tx, patient, name string, args json.RawMessage, state supportAgentState) (supportAgentTool, supportAgentState, error) {
	switch name {
	case "get_today_tasks":
		if err := decodeSupportToolArgs(args, &struct{}{}); err != nil {
			return supportAgentTool{}, state, err
		}
		result, err := supportReadTodayTasks(ctx, tx, patient)
		if err != nil {
			return supportAgentTool{}, state, err
		}
		state.Focus = "tasks"
		state.NextStep = "open_diary"
		tool, err := supportToolResult(name, "Проверены задания на сегодня и отметки заполнения из вашего профиля.", result)
		return tool, state, err
	case "explain_diary":
		var in struct {
			Kind  string  `json:"kind"`
			Field *string `json:"field"`
		}
		if err := decodeSupportToolArgs(args, &in, "kind", "field"); err != nil {
			return supportAgentTool{}, state, err
		}
		if !supportContainsEnum(in.Kind, "ema", "csd") {
			return supportAgentTool{}, state, errors.New("invalid diary kind")
		}
		field := "overview"
		if in.Field != nil {
			field = *in.Field
		}
		explanations := supportDiaryExplanations(in.Kind)
		explanation, ok := explanations[field]
		if !ok {
			return supportAgentTool{}, state, errors.New("invalid diary field")
		}
		state.Focus = in.Kind
		state.NextStep = "open_diary"
		tool, err := supportToolResult(name, "Разъяснены вопросы дневника "+strings.ToUpper(in.Kind)+" без изменения ответов.", map[string]any{"kind": in.Kind, "field": field, "text": explanation})
		return tool, state, err
	case "propose_support_step":
		var in struct {
			Kind  string  `json:"kind"`
			Diary *string `json:"diary_kind"`
		}
		if err := decodeSupportToolArgs(args, &in, "kind", "diary_kind"); err != nil {
			return supportAgentTool{}, state, err
		}
		if !supportContainsEnum(in.Kind, "talk", "small_step", "doctor_message", "open_diary") || in.Diary != nil && !supportContainsEnum(*in.Diary, "ema", "csd") || in.Kind != "open_diary" && in.Diary != nil {
			return supportAgentTool{}, state, errors.New("invalid support step")
		}
		state.NextStep = in.Kind
		replies := map[string]string{
			"talk":           "Я могу выслушать вас и помочь разобраться с дневником или заданиями. Что сейчас беспокоит больше всего?",
			"small_step":     "Похоже, сейчас непросто. Если вам подходит, можно ненадолго остановиться, удобно сесть и обратить внимание на окружающие предметы. Не обязательно решать всё сразу. Хотите рассказать, что тревожит больше всего?",
			"doctor_message": "Можно открыть «Переписка» и своими словами написать врачу, что вас беспокоит и какой вопрос хочется обсудить. Я не отправляю сообщение за вас; текст и отправку выбираете вы.",
			"open_diary":     "Можно открыть дневник и заполнить ответы самостоятельно. Я могу объяснить непонятное поле, но не выбираю ответы и не сохраняю их за вас.",
		}
		if in.Diary != nil {
			state.Focus = *in.Diary
		} else if in.Kind == "doctor_message" {
			state.Focus = "doctor"
		} else if in.Kind != "open_diary" {
			state.Focus = "support"
		}
		tool, err := supportToolResult(name, "Предложен небольшой следующий шаг; действия выполняете только вы.", map[string]any{"kind": in.Kind, "diary_kind": in.Diary, "text": replies[in.Kind]})
		return tool, state, err
	default:
		return supportAgentTool{}, state, errors.New("unknown support tool")
	}
}
func supportReadTodayTasks(ctx context.Context, tx pgx.Tx, patient string) (supportTodayTasks, error) {
	result := supportTodayTasks{Items: []supportTodayTask{}}
	if err := tx.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date::text,timezone FROM users WHERE id=$1 AND active AND role='patient'`, patient).Scan(&result.Date, &result.Timezone); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.kind,a.due_time::text,e.id,CASE WHEN e.id IS NOT NULL THEN 'completed' WHEN (now() AT TIME ZONE u.timezone)::time>=a.due_time THEN 'due' ELSE 'upcoming' END FROM assignments a JOIN users u ON u.id=a.patient_id LEFT JOIN LATERAL (SELECT id FROM current_entries WHERE patient_id=a.patient_id AND kind=a.kind AND local_date=(now() AT TIME ZONE u.timezone)::date ORDER BY created_at DESC,id DESC LIMIT 1) e ON true WHERE a.patient_id=$1 AND a.status='active' AND (now() AT TIME ZONE u.timezone)::date BETWEEN a.start_date AND a.end_date AND mod(((now() AT TIME ZONE u.timezone)::date-a.start_date),a.interval_days)=0 ORDER BY a.due_time,a.kind,a.id LIMIT 21`, patient)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var task supportTodayTask
		if err = rows.Scan(&task.AssignmentID, &task.Kind, &task.DueTime, &task.EntryID, &task.Status); err != nil {
			return result, err
		}
		task.Available = supportContainsEnum(task.Kind, "ema", "csd")
		result.Items = append(result.Items, task)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > 20 {
		result.Items = result.Items[:20]
		result.Truncated = true
	}
	if len(result.Items) == 0 {
		result.Text = "На сегодня в вашем профиле нет заданий по активному расписанию. План можно уточнить у врача в разделе «Переписка»."
		return result, nil
	}
	lines := []string{"Сегодня, " + result.Date + " (" + result.Timezone + "):"}
	labels := map[string]string{"ema": "EMA — дневник настроения", "csd": "CSD — дневник сна", "madrs": "MADRS"}
	statuses := map[string]string{"completed": "уже заполнено", "due": "время заполнения наступило", "upcoming": "запланировано позже сегодня"}
	for _, task := range result.Items {
		label := labels[task.Kind]
		if label == "" {
			label = "Задание врача"
		}
		line := fmt.Sprintf("%s, %s — %s.", label, task.DueTime, statuses[task.Status])
		if !task.Available {
			line += " Форма пока недоступна в приложении; уточните у врача."
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Дневники можно открыть в разделе «Сегодня». Ответы выбираете и сохраняете только вы.")
	result.Text = strings.Join(lines, "\n")
	return result, nil
}
func supportDiaryExplanations(kind string) map[string]string {
	if kind == "ema" {
		return map[string]string{
			"overview": supportDemoReply("EMA"), "mood": "В вопросе о настроении ориентируйтесь на подписи шкалы и отметьте своё самочувствие за указанный в форме период. Это наблюдение, а не диагноз.",
			"anhedonia": "Здесь речь о том, насколько трудно испытывать интерес или удовольствие от обычных дел. Ориентируйтесь на подписи шкалы в форме.",
			"energy":    "Энергия — ощущение сил для обычных дел. В этой форме более высокий балл означает больше энергии; ориентируйтесь на подписи шкалы.",
			"anxiety":   "Отметьте выраженность тревоги по подписям шкалы, как вы её ощущаете. Не нужно подбирать «правильный» ответ.",
			"suicidal":  "Это прямой вопрос о мыслях о самоубийстве. Ответьте честно за указанный в форме период. Положительный ответ в EMA передаётся врачу через отдельный механизм сигналов; обычная переписка с ботом его не заменяет.",
		}
	}
	return map[string]string{
		"overview": supportDemoReply("CSD дневник сна"), "bedtime": "Укажите время, когда вы легли в постель, даже если уснуть пытались позже.", "sleep_attempt": "Укажите время, когда вы начали пытаться уснуть. Оно может отличаться от времени отхода в постель.", "latency": "Укажите примерно, сколько минут прошло от попытки уснуть до засыпания.", "awakenings": "Укажите число пробуждений за ночь после первоначального засыпания.", "awake_minutes": "Укажите суммарное время бодрствования во время ночных пробуждений, в минутах.", "final_wake": "Укажите время последнего утреннего пробуждения, после которого вы уже не спали.", "rise_time": "Укажите время, когда вы встали с постели, даже если проснулись раньше.", "quality": "Оцените качество сна по подписям шкалы в форме. Здесь важно ваше субъективное ощущение.", "rested": "Оцените ощущение отдыха после сна по подписям шкалы в форме.",
	}
}
