import { useRef, useState, type FormEvent } from "react";
import { api, refreshData, type Model } from "../api-client";
import { ErrorBox, Modal, Notice, shortDate, timeLabel } from "./common";
import { useDraftGuard, confirmDiscard } from "./navigation";
import { usePages, PageControls, QueryFailure } from "./pagination";
type Schedule = {
  id: string;
  kind: "ema" | "csd";
  start_date: string;
  end_date: string;
  due_time: string;
  interval_days: number;
  status: "active" | "paused" | "cancelled";
  created_at?: string;
};
type Snapshot = Model<"ScheduleHistory">;
function ScheduleHistory({ id }: { id: string }) {
  const page = usePages<Snapshot>(`/schedules/${id}/history`);
  return (
    <>
      <QueryFailure error={page.error} retry={page.reload} />
      {page.items.map((item, i) => (
        <article className="schedule-row" key={item.id ?? i}>
          <div>
            <strong>
              {(
                {
                  created: "Создано назначение",
                  updated: "Изменено назначение",
                  paused: "Приостановлено",
                  active: "Возобновлено",
                  cancelled: "Отменено",
                } as Record<string, string>
              )[item.operation] ?? "Изменение расписания"}
            </strong>
            <span>{item.created_at ? timeLabel(item.created_at) : ""}</span>
            <p>{item.actor_name}</p>
            <p>
              {item.snapshot.kind === "ema" ? "Самочувствие" : "Сон"}:{" "}
              {shortDate(item.snapshot.start_date)} —{" "}
              {shortDate(item.snapshot.end_date)},{" "}
              {item.snapshot.due_time.slice(0, 5)}; каждые{" "}
              {item.snapshot.interval_days} дн.
            </p>
          </div>
        </article>
      ))}
      <PageControls
        total={page.total}
        shown={page.items.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
    </>
  );
}
export function Schedules({
  patientID,
  today,
}: {
  patientID: string;
  today: string;
}) {
  const page = usePages<Schedule>(`/patients/${patientID}/schedules`);
  const [editing, setEditing] = useState<Schedule | null>(null);
  const [history, setHistory] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const keys = useRef(new Map<string, string>());
  useDraftGuard(dirty, "Расписание");
  async function write(path: string, body: Record<string, unknown>) {
    const signature = JSON.stringify([path, body]);
    let key = keys.current.get(signature);
    if (!key) {
      key = crypto.randomUUID();
      keys.current.set(signature, key);
    }
    await api(path, { ...body, request_key: key });
    keys.current.delete(signature);
    refreshData();
    page.reload();
  }
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      await write(
        editing
          ? `/schedules/${editing.id}/update`
          : `/patients/${patientID}/schedules`,
        {
          kind: f.get("kind"),
          start_date: f.get("start_date"),
          end_date: f.get("end_date"),
          due_time: f.get("due_time"),
          interval_days: Number(f.get("interval_days")),
        },
      );
      setDirty(false);
      setEditing(null);
      setNotice("Расписание сохранено");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function status(item: Schedule, value: Schedule["status"]) {
    setBusy(true);
    setError("");
    try {
      await write(`/schedules/${item.id}/status`, { status: value });
      setNotice("Статус расписания изменён");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="panel">
      <h2>Расписание наблюдения</h2>
      <p className="helper">
        Можно назначить отдельные интервалы, приостановить или отменить
        назначение. Время указано в часовом поясе пациента.
      </p>
      <QueryFailure error={page.error} retry={page.reload} />
      <ErrorBox message={error} />
      {notice ? <Notice>{notice}</Notice> : null}
      {page.loading ? (
        <p role="status">Загружаем расписание…</p>
      ) : page.items.length === 0 ? (
        <p>Назначений пока нет.</p>
      ) : (
        page.items.map((item) => (
          <article className="schedule-row" key={item.id}>
            <div>
              <strong>
                {item.kind === "ema" ? "Самочувствие (EMA)" : "Сон (CSD)"}
              </strong>
              <span>
                {shortDate(item.start_date)} — {shortDate(item.end_date)},{" "}
                {item.due_time.slice(0, 5)}; каждые {item.interval_days ?? 1}{" "}
                дн.
              </span>
              <span className="badge neutral">
                {item.status === "paused"
                  ? "Приостановлено"
                  : item.status === "cancelled"
                    ? "Отменено"
                    : "Активно"}
              </span>
            </div>
            <div className="button-row">
              <button
                className="text-button"
                disabled={busy}
                onClick={() => {
                  if (confirmDiscard()) {
                    setDirty(false);
                    setEditing(item);
                  }
                }}
              >
                Изменить
              </button>
              {item.status !== "cancelled" ? (
                <>
                  <button
                    className="text-button"
                    disabled={busy}
                    onClick={() =>
                      void status(
                        item,
                        item.status === "paused" ? "active" : "paused",
                      )
                    }
                  >
                    {item.status === "paused" ? "Возобновить" : "Приостановить"}
                  </button>
                  <button
                    className="text-button"
                    disabled={busy}
                    onClick={() => void status(item, "cancelled")}
                  >
                    Отменить
                  </button>
                </>
              ) : null}
              <button
                className="text-button"
                onClick={() => setHistory(item.id)}
              >
                История
              </button>
            </div>
          </article>
        ))
      )}
      <PageControls
        total={page.total}
        shown={page.items.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
      <h3>{editing ? "Изменить назначение" : "Новое назначение"}</h3>
      <form
        key={editing?.id ?? "new"}
        onSubmit={submit}
        onChange={() => setDirty(true)}
      >
        <label>
          Дневник
          <select name="kind" defaultValue={editing?.kind ?? "csd"}>
            <option value="csd">Сон (CSD)</option>
            <option value="ema">Самочувствие (EMA)</option>
          </select>
        </label>
        <div className="form-grid">
          <label>
            Начало
            <input
              type="date"
              name="start_date"
              defaultValue={editing?.start_date ?? today}
              required
            />
          </label>
          <label>
            Окончание
            <input
              type="date"
              name="end_date"
              defaultValue={editing?.end_date ?? today}
              required
            />
          </label>
          <label>
            Время напоминания
            <input
              type="time"
              name="due_time"
              defaultValue={editing?.due_time.slice(0, 5) ?? "09:00"}
              required
            />
          </label>
          <label>
            Повторять каждые, дней
            <input
              type="number"
              name="interval_days"
              min={1}
              max={365}
              defaultValue={editing?.interval_days ?? 1}
              required
            />
          </label>
        </div>
        <div className="button-row">
          <button className="primary" disabled={busy}>
            {busy ? "Сохраняем…" : "Сохранить расписание"}
          </button>
          {editing ? (
            <button
              className="secondary"
              type="button"
              onClick={() => {
                if (confirmDiscard()) {
                  setDirty(false);
                  setEditing(null);
                }
              }}
            >
              К новому назначению
            </button>
          ) : null}
        </div>
      </form>
      <p className="helper">
        MADRS, PHQ-9, HDRS и C-SSRS не назначаются: утверждённые полные формы
        пока не предоставлены.
      </p>
      {history ? (
        <Modal title="История расписания" onClose={() => setHistory(null)}>
          <ScheduleHistory id={history} />
        </Modal>
      ) : null}
    </section>
  );
}
