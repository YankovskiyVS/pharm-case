import { StrictMode, useState, useRef, type FormEvent } from "react";
import { createRoot } from "react-dom/client";
import {
  LayoutDashboard,
  Users,
  Bell,
  Plus,
  Search,
  ChevronRight,
  ArrowLeft,
  ClipboardCheck,
  Activity,
  ShieldCheck,
  BookOpen,
  Download,
} from "lucide-react";
import {
  api,
  downloadReport,
  endpoint,
  refreshData,
  type DoctorDashboard,
  type Detail,
  type Alert,
  type Model,
} from "../../../packages/api-client";
import {
  SessionProvider,
  useSession,
  useQuery,
} from "../../../packages/ui/session";
import {
  Brand,
  Account,
  Login,
  Loading,
  ErrorBoundary,
  ErrorBox,
  Empty,
  Modal,
  Notice,
  TrendChart,
  SleepTrendChart,
  initials,
  shortDate,
  timeLabel,
} from "../../../packages/ui/common";
import { ClinicalWorkspace } from "../../../packages/ui/clinical";
import { AudioWorkspace } from "../../../packages/ui/audio";
import { Messages } from "../../../packages/ui/messages";
import { EMASummary, SleepSummary } from "../../../packages/ui/summaries";
import { Notifications } from "../../../packages/ui/notifications";
import {
  navigate,
  useUrlState,
  useDraftGuard,
} from "../../../packages/ui/navigation";
import {
  usePages,
  PageControls,
  QueryFailure,
} from "../../../packages/ui/pagination";
import { Schedules } from "../../../packages/ui/schedules";
import { ReviewHistory } from "../../../packages/ui/reviews";
import { History } from "../../../packages/ui/history";
import { PubMedResearch } from "../../../packages/ui/pubmed";
import "../../../packages/ui/styles.css";

type ClinicalAlert = Alert & {
  severity?: "critical" | "warning";
  title?: string;
  reason?: string;
  metric?: string;
  baseline_value?: number | null;
  current_value?: number | null;
  delta?: number | null;
  period_start?: string | null;
  period_end?: string | null;
  rule_version?: string;
};

function alertDescription(alert: ClinicalAlert) {
  if (alert.type === "DETERIORATION") {
    if (alert.reason) return alert.reason;
    const values =
      alert.baseline_value != null && alert.current_value != null
        ? `: ${alert.baseline_value} → ${alert.current_value}${alert.delta != null ? ` (изменение +${alert.delta})` : ""}`
        : "";
    return `Ухудшение динамики настроения по EMA${values}`;
  }
  return "Положительный ответ о суицидальных мыслях";
}

function AlertList({
  alerts,
  onPatient,
}: {
  alerts: Alert[];
  onPatient?: (id: string) => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [closing, setClosing] = useState<Alert | null>(null);
  async function action(alert: Alert, note = "") {
    setBusy(alert.id);
    setError("");
    try {
      await endpoint("/alerts/{id}/actions", "post", {
        path: { id: alert.id },
        body: { action: alert.status === "new" ? "ack" : "resolved", note },
      });
      setClosing(null);
      refreshData();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  return (
    <>
      <ErrorBox message={error} />
      {!alerts.length ? (
        <Empty>
          <ShieldCheck size={25} />
          <p>Активных сигналов нет. Новые ответы появятся здесь.</p>
        </Empty>
      ) : (
        <div className="alert-list">
          {alerts.map((a) => {
            const alert = a as ClinicalAlert;
            return (
              <article
                className={`alert-row ${a.status === "resolved" ? "resolved" : ""} ${alert.type === "DETERIORATION" ? "deterioration" : ""}`}
                key={a.id}
                id={`alert-${a.id}`}
              >
                <span className="alert-indicator">
                  <Bell size={20} />
                </span>
                <div>
                  <div className="alert-title">
                    <strong>{a.patient_name ?? "Сигнал пациента"}</strong>
                    <span
                      className={`badge ${a.status === "new" ? "danger" : a.status === "ack" ? "warning" : "success"}`}
                    >
                      {a.status === "new"
                        ? "Новый"
                        : a.status === "ack"
                          ? "Ознакомлен"
                          : "Закрыт"}
                    </span>
                  </div>
                  <p>{alertDescription(alert)}</p>
                  {alert.type === "DETERIORATION" ? (
                    <p className="helper alert-context">
                      {alert.period_start && alert.period_end
                        ? `Период: ${shortDate(alert.period_start)} — ${shortDate(alert.period_end)}. `
                        : ""}
                      Требуется оценка врача; индикатор не является диагнозом.
                    </p>
                  ) : null}
                  {(
                    a as Alert & {
                      actions?: {
                        id: string;
                        action: string;
                        note: string;
                        actor_name: string;
                        created_at: string;
                      }[];
                    }
                  ).actions?.length ? (
                    <details>
                      <summary>История действий врача</summary>
                      {(
                        a as Alert & {
                          actions: {
                            id: string;
                            action: string;
                            note: string;
                            actor_name: string;
                            created_at: string;
                          }[];
                        }
                      ).actions.map((action) => (
                        <p key={action.id}>
                          {action.actor_name}:{" "}
                          {action.action === "ack"
                            ? "Ознакомлен"
                            : "Записано действие"}{" "}
                          — {timeLabel(action.created_at)}
                          {action.note ? ` — ${action.note}` : ""}
                        </p>
                      ))}
                    </details>
                  ) : null}

                  <small>
                    {timeLabel(a.created_at)}
                    {a.overdue ? " · Без подтверждения более 5 минут" : ""}
                  </small>
                  {onPatient && a.patient_id ? (
                    <button
                      className="text-button"
                      onClick={() => onPatient(a.patient_id!)}
                    >
                      Открыть профиль
                    </button>
                  ) : null}
                </div>
                {a.status !== "resolved" ? (
                  <button
                    className={
                      a.status === "new" ? "primary small" : "secondary small"
                    }
                    disabled={busy === a.id}
                    onClick={() =>
                      a.status === "new" ? void action(a) : setClosing(a)
                    }
                  >
                    {a.status === "new" ? "Ознакомлен" : "Записать действие"}
                  </button>
                ) : null}
              </article>
            );
          })}
        </div>
      )}
      {closing ? (
        <Modal title="Закрыть сигнал" onClose={() => setClosing(null)}>
          <p className="muted">
            Зафиксируйте выполненное действие. Закрытие сигнала не изменяет
            ответы пациента.
          </p>
          <form
            onSubmit={(e: FormEvent<HTMLFormElement>) => {
              e.preventDefault();
              void action(
                closing,
                String(new FormData(e.currentTarget).get("note")),
              );
            }}
          >
            <label>
              Выполненное действие
              <textarea name="note" required maxLength={2000} rows={4} />
            </label>
            <button className="primary" disabled={!!busy}>
              Сохранить и закрыть
            </button>
            <ErrorBox message={error} />
          </form>
        </Modal>
      ) : null}
    </>
  );
}
function Invite({ onClose }: { onClose: () => void }) {
  const [link, setLink] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const f = new FormData(e.currentTarget);
    try {
      const result = await api<Model<"InvitationResult">>(
        "/doctor/invitations",
        { name: f.get("name"), email: f.get("email") },
      );
      setLink(`${location.origin}/patient/#invite=${result.token}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal title="Пригласить пациента" onClose={onClose}>
      {link ? (
        <>
          <Notice>Приглашение создано на 7 дней.</Notice>
          <p>
            Если отправка почты настроена, система отправит приглашение на адрес
            пациента. Ссылку также можно передать лично. Срок действия — 7 дней.
          </p>
          <label>
            Ссылка приглашения
            <input readOnly value={link} />
          </label>
          <button
            className="primary"
            onClick={() =>
              navigator.clipboard
                .writeText(link)
                .then(() => setCopied(true))
                .catch(() =>
                  setError("Не удалось скопировать. Выделите ссылку вручную."),
                )
            }
          >
            {copied ? "Скопировано" : "Скопировать ссылку"}
          </button>
        </>
      ) : (
        <form onSubmit={submit}>
          <label>
            Имя пациента
            <input name="name" minLength={2} maxLength={120} required />
          </label>
          <label>
            Электронная почта
            <input name="email" type="email" required />
          </label>
          <button className="primary" disabled={busy}>
            {busy ? "Создаём…" : "Создать приглашение"}
            <Plus size={17} />
          </button>
        </form>
      )}
      <ErrorBox message={error} />
    </Modal>
  );
}
function PatientWorkspace({ id, onBack }: { id: string; onBack: () => void }) {
  const { data, error, loading } = useQuery<Detail>(`/patients/${id}`);
  const [tab, setTab] = useUrlState("tab", "overview");
  const [message, setMessage] = useState("");
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [exporting, setExporting] = useState(false);
  const reviewKey = useRef(crypto.randomUUID());
  const [reviewDirty, setReviewDirty] = useState(false);
  useDraftGuard(reviewDirty, "Осмотр профиля");
  async function review(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setFailure("");
    const form = new FormData(e.currentTarget);
    try {
      await api(`/patients/${id}/reviews`, {
        action: form.get("action"),
        note: form.get("note"),
        snapshot_token: data?.review_snapshot,
        request_key: reviewKey.current,
      });
      setReviewDirty(false);
      reviewKey.current = crypto.randomUUID();
      setMessage("Осмотр профиля сохранён");
      refreshData();
    } catch (e) {
      setFailure((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function exportDiaries() {
    setExporting(true);
    setFailure("");
    try {
      const patientName = data?.patient.name
        .replace(/[^a-zA-Zа-яА-ЯёЁ0-9]+/g, "-")
        .replace(/^-|-$/g, "");
      await downloadReport(
        `/patients/${id}/export?format=csv`,
        `psitech-diaries-${patientName || id}-${data?.today ?? "export"}.csv`,
      );
    } catch (e) {
      setFailure((e as Error).message);
    } finally {
      setExporting(false);
    }
  }
  if (!data)
    return (
      <>
        <button className="text-button" onClick={onBack}>
          <ArrowLeft size={17} />К списку пациентов
        </button>
        {loading ? <Loading /> : <ErrorBox message={error} />}
      </>
    );
  const lastSleep = data.entries.find((e) => e.kind === "csd");
  const sleep = lastSleep?.scores as Model<"SleepResult"> | undefined;
  return (
    <>
      <button className="text-button back" onClick={onBack}>
        <ArrowLeft size={17} />
        Все пациенты
      </button>
      <header className="patient-heading">
        <span className="avatar large">{initials(data.patient.name)}</span>
        <div>
          <h1>{data.patient.name}</h1>
          <p>
            {data.patient.email} · {data.patient.timezone}
          </p>
        </div>
        <div className="patient-heading-actions">
          <span className="badge neutral">Под наблюдением</span>
          <button
            className="secondary"
            disabled={exporting}
            onClick={() => void exportDiaries()}
          >
            <Download size={17} />
            {exporting ? "Готовим CSV…" : "Скачать дневники CSV"}
          </button>
        </div>
      </header>
      <div className="tabs" role="tablist" aria-label="Разделы профиля">
        {[
          ["overview", "Обзор"],
          ["history", "Дневники"],
          ["messages", "Переписка"],
          ["audio", "Голос"],
          ["clinical", "Клинический контекст"],
          ["schedule", "Расписание"],
        ].map(([key, label]) => (
          <button
            role="tab"
            aria-selected={tab === key}
            key={key}
            onClick={() => {
              setTab(key);
              setMessage("");
              setFailure("");
            }}
          >
            {label}
          </button>
        ))}
      </div>
      <ErrorBox message={error || failure} />
      {message ? <Notice>{message}</Notice> : null}
      {tab === "audio" ? <AudioWorkspace key={id} patientID={id} /> : null}
      {tab === "clinical" ? (
        <ClinicalWorkspace key={id} patientID={id} today={data.today} />
      ) : null}
      {tab === "overview" ? (
        <>
          <div className="detail-metrics">
            <div>
              <span>Настроение за 3 дня</span>
              <strong>
                {data.summary.mood?.toFixed(1) ?? "—"}
                <small>/10</small>
              </strong>
              <small>{data.summary.count} из 3 дней заполнено</small>
            </div>
            <div>
              <span>Изменение настроения</span>
              <strong>
                {data.summary.mood_delta == null
                  ? "—"
                  : `${data.summary.mood_delta > 0 ? "+" : ""}${data.summary.mood_delta}`}
              </strong>
              <small>День 3 минус день 1; выше — хуже</small>
            </div>
            <div>
              <span>Последний сон</span>
              <strong>
                {sleep?.tib == null ? "—" : `${Math.round(sleep.tib)} `}
                <small>мин в постели</small>
              </strong>
              <small>
                Эффективность{" "}
                {sleep?.se == null ? "—" : Math.round(sleep.se * 100) + "%"}
              </small>
            </div>
          </div>
          <section className="panel">
            <div className="section-heading">
              <h2>Динамика самочувствия</h2>
              <span className="muted">Последние 14 записей</span>
            </div>
            <TrendChart entries={data.entries} />
          </section>
          <EMASummary summary={data.summary} clinical />
          <SleepSummary summary={data.sleep_summary} />
          <section className="panel">
            <SleepTrendChart entries={data.entries} />
          </section>
          <section className="panel">
            <div className="section-heading">
              <h2>
                <Bell size={19} />
                Сигналы пациента
              </h2>
            </div>
            <AlertQueue patientID={id} />
          </section>
          <section className="panel">
            <h2>Еженедельный осмотр профиля</h2>
            <form
              className="review-form"
              onSubmit={review}
              onChange={() => {
                setReviewDirty(true);
                reviewKey.current = crypto.randomUUID();
              }}
            >
              <label>
                Действие
                <select name="action">
                  <option value="reviewed">Ознакомлен с профилем</option>
                  <option value="contact">
                    Инициировать связь с пациентом
                  </option>
                </select>
              </label>
              <label>
                Заметка
                <textarea name="note" maxLength={2000} rows={2} />
              </label>
              <button className="primary" disabled={busy}>
                <ClipboardCheck size={18} />
                Сохранить осмотр
              </button>
            </form>
            <ReviewHistory patientID={id} />
          </section>
        </>
      ) : null}
      {tab === "history" ? (
        <section className="panel">
          <History
            patientID={data.patient.id}
            entries={data.entries}
            today={data.today}
            timezone={data.patient.timezone}
          />
        </section>
      ) : null}
      {tab === "messages" ? (
        <section className="panel">
          <Messages patientID={id} team={data.care_team} />
        </section>
      ) : null}
      {tab === "schedule" ? (
        <Schedules patientID={id} today={data.today} />
      ) : null}
    </>
  );
}
function DoctorApp() {
  const { user, loading, error } = useSession();
  const [page] = useUrlState("page", "overview");
  const [selected] = useUrlState("patient", "");
  const setSelected = (id: string | null) =>
    navigate({ patient: id, tab: id ? "overview" : null });
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [invite, setInvite] = useState(false);
  const {
    data,
    error: queryError,
    loading: dashboardLoading,
    reload: reloadDashboard,
  } = useQuery<
    DoctorDashboard & {
      alerts_total?: number;
      patients_total?: number;
      pending_reviews_total?: number;
    }
  >(user?.role === "doctor" ? "/dashboard" : null);
  if (loading) return <Loading />;
  if (!user) return <Login portal="doctor" />;
  if (user.role !== "doctor")
    return (
      <main className="fatal">
        <h1>Это кабинет врача</h1>
        <p>Вы вошли как пациент.</p>
        <a className="primary" href="/patient/">
          Открыть мой дневник
        </a>
        <Account />
      </main>
    );
  const patients = (data?.patients ?? []).filter(
    (p) =>
      p.name.toLocaleLowerCase().includes(search.toLocaleLowerCase()) &&
      (filter !== "alerts" || p.open_alerts > 0),
  );
  const nav = [
    ["overview", "Обзор", LayoutDashboard],
    ["patients", "Пациенты", Users],
    ["alerts", "Сигналы", Bell],
    ["research", "Научный раздел", BookOpen],
  ] as const;
  return (
    <div className="doctor-shell">
      <aside className="sidebar">
        <Brand />
        <span className="workspace-label">Рабочее пространство</span>
        <nav aria-label="Основная навигация">
          {nav.map(([key, label, Icon]) => (
            <button
              className={page === key ? "active" : ""}
              key={key}
              onClick={() => {
                navigate({ page: key, patient: null, tab: null });
              }}
            >
              <Icon size={20} />
              {label}
              {key === "alerts" &&
              (data?.alerts_total ?? data?.alerts.length) ? (
                <span className="nav-count">
                  {data.alerts_total ?? data.alerts.length}
                </span>
              ) : null}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="care-note">
            <Activity size={20} />
            <p>
              Данные помогают наблюдать.
              <br />
              Решения принимает врач.
            </p>
          </div>
          <Account />
        </div>
      </aside>
      <div className="doctor-body">
        <header className="topbar">
          <span>Кабинет врача</span>
          <div>
            <Notifications />
            <span className="connection-dot" />
            Локальная версия{" "}
            <span className="topbar-date">
              {new Date().toLocaleDateString("ru-RU", {
                day: "numeric",
                month: "long",
              })}
            </span>
          </div>
        </header>
        <main className="doctor-main">
          <ErrorBox message={error || (data ? queryError : "")} />
          {selected ? (
            <PatientWorkspace
              key={selected}
              id={selected}
              onBack={() => setSelected(null)}
            />
          ) : (
            <>
              <header className="page-heading">
                <div>
                  <p className="greeting">
                    Здравствуйте, {user.name.split(" ")[0]}
                  </p>
                  <h1>
                    {page === "alerts"
                      ? "Сигналы пациентов"
                      : page === "research"
                        ? "Научный раздел"
                        : page === "patients"
                          ? "Ваши пациенты"
                          : "Обзор наблюдения"}
                  </h1>
                  <p className="muted">
                    {page === "alerts"
                      ? "Ответы, которые требуют вашего внимания."
                      : page === "research"
                        ? "Поиск медицинских публикаций и аннотаций в PubMed."
                        : "Самочувствие, сон и важные изменения — в одном месте."}
                  </p>
                </div>
                {page !== "research" ? (
                  <button className="primary" onClick={() => setInvite(true)}>
                    <Plus size={18} />
                    Пригласить пациента
                  </button>
                ) : null}
              </header>
              {page === "research" ? (
                <PubMedResearch />
              ) : page === "overview" ? (
                <div className="overview-metrics">
                  <div>
                    <span className="metric-icon">
                      <Users size={21} />
                    </span>
                    <span>
                      Под наблюдением
                      <strong>
                        {data?.patients_total ?? data?.patients.length ?? "—"}
                      </strong>
                    </span>
                  </div>
                  <div>
                    <span className="metric-icon red">
                      <Bell size={21} />
                    </span>
                    <span>
                      Активные сигналы
                      <strong>
                        {data?.alerts_total ?? data?.alerts.length ?? "—"}
                      </strong>
                    </span>
                  </div>
                  <div>
                    <span className="metric-icon gold">
                      <ClipboardCheck size={21} />
                    </span>
                    <span>
                      Ожидают осмотра
                      <strong>
                        {data?.pending_reviews_total ??
                          data?.patients.filter(
                            (p) =>
                              !p.reviewed_at ||
                              Date.now() - new Date(p.reviewed_at).getTime() >
                                7 * 86400000,
                          ).length ??
                          "—"}
                      </strong>
                    </span>
                  </div>
                </div>
              ) : null}
              {page === "research" ? null : dashboardLoading && !data ? (
                <Loading />
              ) : queryError && !data ? (
                <QueryFailure error={queryError} retry={reloadDashboard} />
              ) : page === "alerts" ? (
                <section className="panel">
                  <div className="section-heading">
                    <h2>Требуют внимания</h2>
                    <span className="badge neutral">Внутри приложения</span>
                  </div>
                  <p className="helper">
                    При настроенной почтовой интеграции уведомления также
                    отправляются на почту. «Ознакомлен» фиксирует ваше
                    подтверждение.
                  </p>
                  <AlertQueue onPatient={setSelected} />
                </section>
              ) : (
                <section className="panel patients-panel">
                  <div className="section-heading">
                    <h2>
                      Пациенты{" "}
                      <span className="count-label">
                        {data?.patients.length ?? 0}
                      </span>
                    </h2>
                    <div className="table-controls">
                      <label className="search-field">
                        <Search size={17} />
                        <input
                          aria-label="Поиск пациента"
                          placeholder="Найти пациента"
                          value={search}
                          onChange={(e) => setSearch(e.target.value)}
                        />
                      </label>
                      <select
                        aria-label="Фильтр пациентов"
                        value={filter}
                        onChange={(e) => setFilter(e.target.value)}
                      >
                        <option value="all">Все пациенты</option>
                        <option value="alerts">С активным сигналом</option>
                      </select>
                    </div>
                  </div>
                  <div className="table-scroll">
                    <table>
                      <thead>
                        <tr>
                          <th>Пациент</th>
                          <th>Наблюдение</th>
                          <th>EMA за 3 дня</th>
                          <th>Последняя запись</th>
                          <th>Осмотр профиля</th>
                          <th>
                            <span className="sr-only">Открыть</span>
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {patients.map((p) => (
                          <tr key={p.id}>
                            <td>
                              <button
                                className="patient-link"
                                onClick={() => setSelected(p.id)}
                              >
                                <span className="avatar">
                                  {initials(p.name)}
                                </span>
                                <span>
                                  <strong>{p.name}</strong>
                                  <small>{p.email}</small>
                                </span>
                              </button>
                            </td>
                            <td>
                              <span
                                className={`badge ${p.open_alerts ? "danger" : "neutral"}`}
                              >
                                {p.open_alerts ? "Сигнал" : "Наблюдение"}
                              </span>
                            </td>
                            <td>
                              <div className="completion-dots">
                                {[1, 2, 3].map((n) => (
                                  <i
                                    key={n}
                                    className={p.ema_count >= n ? "filled" : ""}
                                  />
                                ))}
                                <span>{p.ema_count}/3</span>
                              </div>
                            </td>
                            <td>
                              {p.last_entry
                                ? timeLabel(p.last_entry)
                                : "Нет записей"}
                            </td>
                            <td>
                              {p.reviewed_at ? (
                                shortDate(p.reviewed_at)
                              ) : (
                                <span className="muted">Не просмотрен</span>
                              )}
                            </td>
                            <td>
                              <button
                                className="icon-button"
                                aria-label={`Открыть профиль ${p.name}`}
                                onClick={() => setSelected(p.id)}
                              >
                                <ChevronRight size={18} />
                              </button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {!patients.length ? (
                    <Empty>
                      {search
                        ? "Пациенты не найдены. Измените запрос."
                        : "Пригласите пациента, чтобы начать наблюдение."}
                    </Empty>
                  ) : null}
                </section>
              )}
              <div className="workspace-footer">
                <ShieldCheck size={16} />
                <span>Доступ только к назначенным вам пациентам</span>
                <span>ПсиTech</span>
              </div>
            </>
          )}
          {invite ? <Invite onClose={() => setInvite(false)} /> : null}
        </main>
      </div>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      <SessionProvider>
        <DoctorApp />
      </SessionProvider>
    </ErrorBoundary>
  </StrictMode>,
);

function AlertQueue({
  patientID,
  onPatient,
}: {
  patientID?: string;
  onPatient?: (id: string) => void;
}) {
  const [status, setStatus] = useState("open");
  const [target] = useUrlState("target", ""),
    [targetType] = useUrlState("target_type", "");
  const direct = useQuery<Alert>(
    target && targetType === "alert" ? `/alerts/${target}` : null,
  );

  const page = usePages<Alert>(
    patientID
      ? `/patients/${patientID}/alerts?status=${status}`
      : `/doctor/alerts?status=${status}`,
  );
  return (
    <>
      <label>
        Показывать
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="open">Открытые сигналы</option>
          <option value="all">Все, включая закрытые</option>
        </select>
      </label>
      {direct.data ? (
        <section className="target-record">
          <h3>Сигнал из уведомления</h3>
          <AlertList alerts={[direct.data]} onPatient={onPatient} />
          <button
            className="text-button"
            onClick={() =>
              navigate({ target: null, target_type: null, anchor: null })
            }
          >
            Убрать выделение
          </button>
        </section>
      ) : null}
      <QueryFailure
        error={page.error || direct.error}
        retry={() => {
          page.reload();
          direct.reload();
        }}
      />
      {page.loading ? (
        <Loading />
      ) : !page.error ? (
        page.items.some((a) => a.id !== direct.data?.id) || !direct.data ? (
          <AlertList
            alerts={page.items.filter((a) => a.id !== direct.data?.id)}
            onPatient={onPatient}
          />
        ) : (
          <p className="helper">Других сигналов на этой странице нет.</p>
        )
      ) : null}
      <PageControls
        total={page.total}
        shown={page.items.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
      <button className="text-button" onClick={page.reload}>
        Обновить сигналы
      </button>
    </>
  );
}
