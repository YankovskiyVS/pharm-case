import { useState, useEffect } from "react";
import { Pencil, Moon, Sun } from "lucide-react";
import {
  api,
  downloadReport,
  type Entry,
  type EMA,
  type CSD,
  type Model,
} from "../api-client";
import { Empty, ErrorBox, shortDate, Modal, timeLabel } from "./common";
import { DiaryForm } from "./diary-form";
import { useUrlState } from "./navigation";
import { usePages, PageControls, QueryFailure, params } from "./pagination";
import { TrendChart, SleepTrendChart, Loading } from "./common";
const labels: Record<string, string> = {
  mood: "Настроение",
  anhedonia: "Отсутствие интереса",
  energy: "Энергия",
  anxiety: "Тревога",
  suicidal: "Суицидальные мысли",
  comment: "Комментарий",
  bedtime: "В постель",
  sleep_attempt: "Время засыпания",
  latency: "До засыпания, мин",
  awakenings: "Число пробуждений",
  awake_minutes: "Бодрствование ночью, мин",
  final_wake: "Окончательное пробуждение",
  after_wake_minutes: "В постели после пробуждения, мин",
  early_wake: "Раньше плана",
  early_minutes: "Раньше плана, мин",
  rise_time: "Подъём",
  total_sleep: "Общее время сна, мин",
  quality: "Качество сна (1–5)",
  rested: "Бодрость (1–5)",
  nap_minutes: "Дремота, мин",
  alcohol: "Алкоголь, порции",
  caffeine: "Кофеин, порции",
};
export function Answers({
  entry,
}: {
  entry: {
    kind: Entry["kind"];
    answers: Record<string, unknown>;
    scores: unknown;
    created_at?: string;
  };
}) {
  const sleep =
    entry.kind === "csd" ? (entry.scores as Model<"SleepResult">) : null;
  return (
    <details className="entry-details">
      <summary>Все ответы</summary>
      <dl>
        {Object.entries(entry.answers).map(([key, value]) => (
          <div key={key}>
            <dt>{labels[key] ?? key}</dt>
            <dd>
              {value == null || value === ""
                ? "Не указано"
                : typeof value === "boolean"
                  ? value
                    ? "Да"
                    : "Нет"
                  : String(value)}
            </dd>
          </div>
        ))}
      </dl>
      {sleep ? (
        <dl aria-label="Расчётные показатели сна">
          <div>
            <dt>Время в постели, мин</dt>
            <dd>{sleep.tib ?? "Не рассчитано"}</dd>
          </div>
          <div>
            <dt>Эффективность сна</dt>
            <dd>
              {sleep.se == null
                ? "Не рассчитано"
                : `${Math.round(Number(sleep.se) * 100)}%`}
            </dd>
          </div>
          <div>
            <dt>Средняя точка сна</dt>
            <dd>{sleep.midpoint ?? "Не рассчитано"}</dd>
          </div>
          <div>
            <dt>WASO — бодрствование ночью, мин</dt>
            <dd>{sleep.waso ?? "Не рассчитано"}</dd>
          </div>
        </dl>
      ) : null}
      {entry.created_at ? (
        <small>Сохранено {timeLabel(entry.created_at)}</small>
      ) : null}
    </details>
  );
}
type Props = {
  entries: Entry[];
  patientID: string;
  editable?: boolean;
  today: string;
  timezone: string;
  showVersions?: boolean;
  assignments?: Model<"Assignment">[];
};
export function History({
  entries,
  patientID,
  editable = false,
  today,
  timezone,
  showVersions = true,
  assignments = [],
}: Props) {
  const [filter, setFilter] = useState("all");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [range, setRange] = useState({ from: "", to: "" });
  const [editing, setEditing] = useState<Entry | null>(null);
  const [backfill, setBackfill] = useState<"ema" | "csd" | null>(null);
  const [versions, setVersions] = useState<Entry[] | null>(null);
  const [error, setError] = useState("");
  const [downloading, setDownloading] = useState(false);
  const page = usePages<Entry>(
    showVersions
      ? params(`/patients/${patientID}/diaries`, {
          kind: filter,
          from: range.from || undefined,
          to: range.to || undefined,
        })
      : null,
  );
  const current = showVersions ? page.items : entries;
  const filtered = current.filter((e) => filter === "all" || e.kind === filter);
  async function openVersions(id: string) {
    setError("");
    try {
      setVersions(
        await api<Entry[]>(`/patients/${patientID}/diaries/${id}/revisions`),
      );
    } catch (e) {
      setError((e as Error).message);
    }
  }
  async function report(format: "json" | "csv") {
    setDownloading(true);
    setError("");
    try {
      await downloadReport(
        `/patients/${patientID}/export?format=${format}`,
        `psitech-report-${today}.${format}`,
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setDownloading(false);
    }
  }
  const available = assignments
    .filter(
      (a) =>
        (!("status" in a) || a.status === "active") && a.start_date <= today,
    )
    .sort((a, b) => b.end_date.localeCompare(a.end_date));
  const assignment = available.find((a) => a.kind === backfill);
  const [entryTarget, clearEntry] = useUrlState("entry", "");
  useEffect(() => {
    if (!showVersions || !entryTarget) return;
    const controller = new AbortController();
    api<Entry[]>(
      `/patients/${patientID}/diaries/${entryTarget}/revisions`,
      undefined,
      controller.signal,
    )
      .then(setVersions)
      .catch((e) => {
        if (e.name !== "AbortError") setError(e.message);
      });
    return () => controller.abort();
  }, [patientID, showVersions, entryTarget]);

  return (
    <section>
      <div className="section-heading">
        <h2>История дневников</h2>
        <select
          aria-label="Тип дневника"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        >
          <option value="all">Все дневники</option>
          <option value="ema">Самочувствие</option>
          <option value="csd">Сон</option>
        </select>
      </div>
      {showVersions ? (
        <>
          <form
            className="history-filters"
            onSubmit={(e) => {
              e.preventDefault();
              setRange({ from, to });
            }}
          >
            <label>
              С даты
              <input
                type="date"
                value={from}
                max={to || today}
                onChange={(e) => setFrom(e.target.value)}
              />
            </label>
            <label>
              По дату
              <input
                type="date"
                value={to}
                min={from}
                max={today}
                onChange={(e) => setTo(e.target.value)}
              />
            </label>
            <button className="secondary">Показать период</button>
          </form>
          <div className="button-row">
            <button
              className="secondary"
              disabled={downloading}
              onClick={() => void report("csv")}
            >
              Скачать полный CSV
            </button>
            <button
              className="secondary"
              disabled={downloading}
              onClick={() => void report("json")}
            >
              Скачать полный JSON
            </button>
            {editable && available.length ? (
              <>
                <button
                  className="text-button"
                  onClick={() => setBackfill("csd")}
                >
                  Заполнить пропущенный день сна
                </button>
                <button
                  className="text-button"
                  onClick={() => setBackfill("ema")}
                >
                  Заполнить пропущенный день EMA
                </button>
              </>
            ) : null}
          </div>
          <p className="helper">
            Экспорт включает полную доступную историю, а не только показанную
            страницу. Графики ниже построены по загруженным записям выбранного
            периода.
          </p>
          <QueryFailure error={page.error} retry={page.reload} />
          {page.loading ? <Loading /> : null}
          {!page.loading && page.items.length > 1 ? (
            <details className="historical-charts">
              <summary>Графики выбранного периода</summary>
              <TrendChart entries={page.items} />
              <SleepTrendChart entries={page.items} />
            </details>
          ) : null}
        </>
      ) : null}
      <ErrorBox message={error} />
      {!filtered.length && !page.loading && !page.error ? (
        <Empty>Здесь появятся заполненные дневники.</Empty>
      ) : (
        <div className="history-list">
          {filtered.map((e) => {
            const a = e.answers as EMA & CSD;
            const score = e.scores as Model<"SleepResult">;
            return (
              <article className="history-row" key={e.id}>
                <span className={`task-icon ${e.kind}`}>
                  {e.kind === "ema" ? <Sun size={20} /> : <Moon size={20} />}
                </span>
                <div>
                  <strong>
                    {e.kind === "ema" ? "Самочувствие" : "Дневник сна"}
                  </strong>
                  <span>
                    {shortDate(e.local_date)}
                    {e.supersedes_id ? " · исправленная запись" : ""}
                  </span>
                  <p>
                    {e.kind === "ema"
                      ? `Настроение ${a.mood} · Энергия ${a.energy} · Тревога ${a.anxiety}`
                      : `Сон ${a.total_sleep ?? "—"} мин · Эффективность ${score.se == null ? "—" : Math.round(score.se * 100) + "%"}`}
                  </p>
                  {a.comment ? (
                    <p className="entry-comment">{a.comment}</p>
                  ) : null}
                  {e.kind === "ema" && a.suicidal ? (
                    <span className="badge danger">
                      Отмечены суицидальные мысли
                    </span>
                  ) : null}
                  <Answers entry={e} />
                  {showVersions && e.supersedes_id ? (
                    <button
                      className="text-button"
                      onClick={() => void openVersions(e.id)}
                    >
                      Предыдущие версии
                    </button>
                  ) : null}
                </div>
                {editable ? (
                  <button
                    className="icon-button"
                    aria-label={`Исправить ${e.kind === "ema" ? "самочувствие" : "сон"} ${e.local_date}`}
                    onClick={() => setEditing(e)}
                  >
                    <Pencil size={16} />
                  </button>
                ) : null}
              </article>
            );
          })}
        </div>
      )}
      {showVersions ? (
        <PageControls
          total={page.total}
          shown={page.items.length}
          cursor={page.cursor}
          loading={page.moreLoading}
          onMore={() => void page.more()}
        />
      ) : null}
      {editing ? (
        <Modal title="Исправить запись" onClose={() => setEditing(null)}>
          <DiaryForm
            kind={editing.kind}
            today={today}
            timezone={editing.timezone}
            existing={editing}
            onSaved={() => setEditing(null)}
          />
        </Modal>
      ) : null}
      {backfill ? (
        <Modal
          title="Заполнить пропущенный день"
          onClose={() => setBackfill(null)}
        >
          {assignment ? (
            <DiaryForm
              kind={backfill}
              today={today}
              timezone={timezone}
              dateRange={assignment}
              onSaved={() => setBackfill(null)}
            />
          ) : (
            <p>
              Активного назначения этого дневника на прошлые даты нет.
              Обратитесь к врачу.
            </p>
          )}
        </Modal>
      ) : null}
      {versions ? (
        <Modal
          title="Версии записи"
          onClose={() => {
            setVersions(null);
            if (entryTarget) clearEntry(null);
          }}
        >
          <History
            entries={versions}
            patientID={patientID}
            today={today}
            timezone={timezone}
            showVersions={false}
          />
        </Modal>
      ) : null}
    </section>
  );
}
