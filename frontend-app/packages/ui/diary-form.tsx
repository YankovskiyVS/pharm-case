import { useState, type FormEvent } from "react";
import { CheckCircle2, Send } from "lucide-react";
import {
  endpoint,
  refreshData,
  type CSD,
  type EMA,
  type Entry,
  type Model,
} from "../api-client";
import { ErrorBox, Notice } from "./common";
import { useDraftGuard } from "./navigation";

type Props = {
  kind: "ema" | "csd";
  today: string;
  timezone: string;
  existing?: Entry;
  dateRange?: { start_date: string; end_date: string };
  onSaved: () => void;
};
const questions = [
  ["mood", "Настроение", "0 — очень хорошее, 10 — крайне подавленное"],
  [
    "anhedonia",
    "Интерес к привычным делам",
    "0 — сильный интерес, 10 — полное отсутствие интереса",
  ],
  ["energy", "Энергия", "0 — крайне истощён, 10 — полон энергии"],
  ["anxiety", "Тревога", "0 — полное спокойствие, 10 — крайняя тревога"],
] as const;
const clocks = [
  ["bedtime", "Во сколько легли в постель?"],
  ["sleep_attempt", "Время засыпания"],
  ["final_wake", "Окончательное пробуждение"],
  ["rise_time", "Во сколько встали с постели?"],
] as const;
const minutes = [
  ["latency", "Время до засыпания, мин"],
  ["awakenings", "Число пробуждений"],
  ["awake_minutes", "Бодрствование ночью, мин"],
  ["after_wake_minutes", "В постели после пробуждения, мин"],
  ["early_minutes", "На сколько минут раньше плана"],
  ["total_sleep", "Общее время сна, мин"],
  ["nap_minutes", "Дневная дремота, мин"],
  ["alcohol", "Алкоголь, порции"],
  ["caffeine", "Кофеин, порции"],
] as const;
export function DiaryForm({
  kind,
  today,
  timezone,
  existing,
  onSaved,
  dateRange,
}: Props) {
  const [entryDate, setEntryDate] = useState(
    existing?.local_date ??
      (dateRange && dateRange.end_date < today ? dateRange.end_date : today),
  );
  const [dirty, setDirty] = useState(false);
  useDraftGuard(dirty, "Дневник");
  const [formID] = useState(() => crypto.randomUUID());
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [risk, setRisk] = useState<"idle" | "sending" | "saved" | "failed">(
    "idle",
  );
  const [saved, setSaved] = useState(false);
  const values = (existing?.answers ?? {}) as Partial<EMA & CSD>;
  async function signal(value: string) {
    if (value !== "yes" || risk === "saved") return;
    setRisk("sending");
    try {
      await endpoint("/patient/risk-signals", "post", {
        body: { form_id: formID },
      });
      setRisk("saved");
      refreshData();
    } catch {
      setRisk("failed");
    }
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const f = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    const n = (name: string) => {
      const value = String(f.get(name) ?? "");
      return value === "" ? null : Number(value);
    };
    let answers: EMA | CSD;
    if (kind === "ema") {
      answers = {
        mood: Number(f.get("mood")),
        anhedonia: Number(f.get("anhedonia")),
        energy: Number(f.get("energy")),
        anxiety: Number(f.get("anxiety")),
        suicidal: f.get("suicidal") === "yes",
        comment: String(f.get("comment") ?? ""),
      };
    } else {
      answers = {
        bedtime: String(f.get("bedtime") ?? ""),
        sleep_attempt: String(f.get("sleep_attempt") ?? ""),
        final_wake: String(f.get("final_wake") ?? ""),
        rise_time: String(f.get("rise_time") ?? ""),
        latency: n("latency"),
        awakenings: n("awakenings"),
        awake_minutes: n("awake_minutes"),
        after_wake_minutes: n("after_wake_minutes"),
        early_wake:
          f.get("early_wake") === "" ? null : f.get("early_wake") === "yes",
        early_minutes: n("early_minutes"),
        total_sleep: n("total_sleep"),
        quality: n("quality"),
        rested: n("rested"),
        nap_minutes: n("nap_minutes"),
        alcohol: n("alcohol"),
        caffeine: n("caffeine"),
        comment: String(f.get("comment") ?? ""),
      };
    }
    const input: Model<"DiaryInput"> = {
      kind,
      local_date: existing?.local_date ?? entryDate,
      timezone,
      form_id: formID,
      answers,
      supersedes_id: existing?.id ?? null,
    };
    try {
      await endpoint("/patient/diaries", "post", { body: input });
      setDirty(false);
      setSaved(true);
      refreshData();
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (saved) return <Notice>Дневник сохранён</Notice>;
  return (
    <form
      className="diary-form"
      onSubmit={submit}
      onChange={() => setDirty(true)}
    >
      <p className="muted">
        {existing
          ? "Исправление сохранится новой версией. Исходные ответы останутся в истории."
          : kind === "ema"
            ? "Как вы чувствовали себя сегодня? Ответьте на пять вопросов."
            : "Вспомните прошедшую ночь. Продолжительность указывайте в минутах."}
      </p>
      <label>
        Дата
        <input
          type="date"
          value={entryDate}
          readOnly={Boolean(existing)}
          min={dateRange?.start_date}
          max={
            dateRange && dateRange.end_date < today ? dateRange.end_date : today
          }
          onChange={(e) => setEntryDate(e.target.value)}
          required
        />
      </label>
      {kind === "ema" ? (
        <>
          {questions.map(([key, label, hint]) => (
            <label className="score-question" key={key}>
              <span>{label}</span>
              <small>{hint}</small>
              <select name={key} defaultValue={values[key] ?? ""} required>
                <option value="" disabled>
                  Выберите оценку
                </option>
                {Array.from({ length: 11 }, (_, n) => (
                  <option value={n} key={n}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
          ))}
          <label>
            Были ли сегодня суицидальные мысли?
            <select
              name="suicidal"
              defaultValue={
                values.suicidal === undefined
                  ? ""
                  : values.suicidal
                    ? "yes"
                    : "no"
              }
              onChange={(e) => void signal(e.target.value)}
              required
            >
              <option value="" disabled>
                Выберите ответ
              </option>
              <option value="no">Нет</option>
              <option value="yes">Да</option>
            </select>
          </label>
          {risk === "sending" ? (
            <p role="status">Сохраняем сигнал для врача…</p>
          ) : null}
          {risk === "saved" ? (
            <Notice>
              Сигнал сохранён и доступен врачу в кабинете. Это не подтверждает,
              что врач уже прочитал его.
            </Notice>
          ) : null}
          {risk === "failed" ? (
            <div>
              <ErrorBox message="Не удалось отправить сигнал врачу. Повторите отправку или сохраните дневник." />
              <button
                type="button"
                className="secondary"
                onClick={() => void signal("yes")}
              >
                Повторить отправку сигнала
              </button>
            </div>
          ) : null}
          {risk !== "idle" ? (
            <p className="safety-copy">
              При непосредственной опасности обратитесь за экстренной помощью.
              Кабинет врача может быть закрыт; не ждите ответа в чате.
            </p>
          ) : null}
        </>
      ) : (
        <>
          <div className="form-grid">
            {clocks.map(([key, label]) => (
              <label key={key}>
                {label}
                <input
                  type="time"
                  name={key}
                  defaultValue={values[key] ?? ""}
                  required={key === "bedtime" || key === "rise_time"}
                />
              </label>
            ))}
            {minutes.map(([key, label]) => (
              <label key={key}>
                {label}
                <input
                  name={key}
                  type="number"
                  min="0"
                  step={key === "awakenings" ? "1" : "any"}
                  defaultValue={values[key] ?? ""}
                  required={key === "total_sleep"}
                />
              </label>
            ))}
            <label>
              Проснулись раньше плана?
              <select
                name="early_wake"
                defaultValue={
                  values.early_wake == null
                    ? ""
                    : values.early_wake
                      ? "yes"
                      : "no"
                }
              >
                <option value="">Не указано</option>
                <option value="no">Нет</option>
                <option value="yes">Да</option>
              </select>
            </label>
            {(["quality", "rested"] as const).map((key) => (
              <label key={key}>
                {key === "quality" ? "Качество сна" : "Бодрость после сна"}
                <select name={key} defaultValue={values[key] ?? ""}>
                  <option value="">Не указано</option>
                  {[1, 2, 3, 4, 5].map((n) => (
                    <option value={n} key={n}>
                      {n}{" "}
                      {n === 1
                        ? "— низкая оценка"
                        : n === 5
                          ? "— высокая оценка"
                          : ""}
                    </option>
                  ))}
                </select>
              </label>
            ))}
          </div>
          <p className="helper">
            Пустые необязательные поля останутся незаполненными. Например, 7
            часов 30 минут = 450 минут.
          </p>
        </>
      )}
      <label>
        Комментарий <span className="muted">(необязательно)</span>
        <textarea
          name="comment"
          maxLength={2000}
          rows={3}
          defaultValue={values.comment ?? ""}
        />
      </label>
      <ErrorBox message={error} />
      <button className="primary full" disabled={busy || risk === "sending"}>
        {busy
          ? "Сохраняем…"
          : existing
            ? "Сохранить исправление"
            : "Сохранить дневник"}
        {busy ? <Send size={18} /> : <CheckCircle2 size={18} />}
      </button>
    </form>
  );
}
