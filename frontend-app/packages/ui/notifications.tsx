import { useState } from "react";
import { Bell } from "lucide-react";
import { api, refreshData, type Model } from "../api-client";
import { usePages, PageControls, QueryFailure } from "./pagination";
import { navigate } from "./navigation";
import { Empty, ErrorBox, timeLabel } from "./common";
type Notification = Model<"Notification"> & {
  id: string;
  target_type: string;
  target_id: string;
  doctor_id?: string | null;
};
const labels: Record<string, string> = {
  "audio.ready": "Готовы результаты анализа аудиозаписи",
  "risk.raised": "Новый сигнал: ответ о суицидальных мыслях",
  "deterioration.raised": "Новый сигнал: ухудшение динамики EMA",
  "diary.submitted": "Дневник заполнен",
  "message.sent": "Новое сообщение",
  "reminder.due": "Пора заполнить назначенный дневник",
  "invitation.accepted": "Регистрация по приглашению завершена",
  "assignment.changed": "Обновлено расписание дневников",
  "review.recorded": "Врач просмотрел профиль и записал действие",
  "alert.updated": "Обновлён статус сигнала",
};
export function Notifications() {
  const page = usePages<Notification>("/notifications?page=true");
  const [open, setOpen] = useState(false);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  async function mark(ids?: string[]) {
    setFailure("");
    setBusy(true);
    try {
      await api("/notifications/read", ids ? { ids } : { all: true });
      page.reload();
      refreshData();
    } catch (e) {
      setFailure((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function visit(n: Notification) {
    const doctor = location.pathname.startsWith("/doctor");
    const tab =
      (
        {
          conversation: "messages",
          audio: "audio",
          diary: "history",
          schedule: "schedule",
          review: "overview",
          alert: "overview",
        } as Record<string, string>
      )[n.target_type] ?? "overview";
    const values: Record<string, string | null> = doctor
      ? { patient: n.patient_id, tab, page: "patients" }
      : {
          page:
            n.target_type === "review"
              ? "progress"
              : tab === "overview"
                ? "today"
                : tab === "schedule"
                  ? "today"
                  : tab,
        };
    values.target_type = n.target_type;
    values.doctor = n.doctor_id ?? null;
    values.entry = n.target_type === "diary" ? n.target_id : null;
    values.target = n.target_id;
    values.anchor =
      n.target_type === "audio"
        ? `audio-${n.target_id}`
        : n.target_type === "alert"
          ? `alert-${n.target_id}`
          : null;
    if (!navigate(values)) return;
    setOpen(false);
    if (!n.is_read) await mark([n.id]);
  }
  return (
    <div className="notification-container">
      <button
        className="icon-button notification-trigger"
        aria-label="Уведомления"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <Bell size={19} />
        {page.unread ? <b>{page.unread}</b> : null}
      </button>
      {open ? (
        <section
          className="notification-popover"
          aria-label="Список уведомлений"
        >
          <div className="section-heading">
            <h2>Уведомления ({page.unread} новых)</h2>
            <button
              className="text-button"
              disabled={busy}
              onClick={() => void mark()}
            >
              Прочитать все
            </button>
          </div>
          <QueryFailure error={page.error} retry={page.reload} />
          <ErrorBox message={failure} />
          {page.loading ? (
            <p role="status">Загружаем уведомления…</p>
          ) : page.items.length ? (
            page.items.map((n) => (
              <article
                key={n.id}
                data-testid={`notification-${n.id}`}
                className={`notification-item ${n.is_read ? "" : "unread"}`}
              >
                <strong>{n.patient_name}</strong>
                <p>{labels[n.type] ?? "Обновление профиля"}</p>
                <small>{timeLabel(n.created_at)}</small>
                <div className="button-row">
                  <button className="text-button" onClick={() => void visit(n)}>
                    Открыть
                  </button>
                  {!n.is_read ? (
                    <button
                      className="text-button"
                      disabled={busy}
                      onClick={() => void mark([n.id])}
                    >
                      Отметить прочитанным
                    </button>
                  ) : null}
                </div>
              </article>
            ))
          ) : !page.error ? (
            <Empty>Уведомлений пока нет.</Empty>
          ) : null}
          <PageControls
            total={page.total}
            shown={page.items.length}
            cursor={page.cursor}
            loading={page.moreLoading}
            onMore={() => void page.more()}
          />
          <p className="helper">
            «Прочитать все» включает более ранние уведомления. Прочтение не
            подтверждает ознакомление с клиническим сигналом.
          </p>
        </section>
      ) : null}
    </div>
  );
}
