import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Sun,
  Moon,
  ArrowRight,
  Check,
  CalendarDays,
  BookOpen,
  MessageCircle,
  Activity,
  Heart,
  Clock,
  Mic,
  HeartHandshake,
} from "lucide-react";
import type { Detail } from "../../../packages/api-client";
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
  TrendChart,
  SleepTrendChart,
  shortDate,
} from "../../../packages/ui/common";
import { DiaryForm } from "../../../packages/ui/diary-form";
import { EMASummary, SleepSummary } from "../../../packages/ui/summaries";
import { Notifications } from "../../../packages/ui/notifications";
import { History } from "../../../packages/ui/history";
import { ReviewHistory } from "../../../packages/ui/reviews";
import { PatientAlertFeedback } from "../../../packages/ui/patient-feedback";
import { AudioWorkspace } from "../../../packages/ui/audio";
import { useUrlState } from "../../../packages/ui/navigation";
import { QueryFailure } from "../../../packages/ui/pagination";
import { Messages } from "../../../packages/ui/messages";
import { SupportChat } from "../../../packages/ui/support-chat";
import "../../../packages/ui/styles.css";
function PatientApp() {
  const { user, loading, error } = useSession();
  const [page, setPage] = useUrlState("page", "today");
  const [form, setForm] = useState<"ema" | "csd" | null>(null);
  const [saved, setSaved] = useState("");
  const {
    data,
    error: queryError,
    loading: dashboardLoading,
    reload,
  } = useQuery<Detail>(user?.role === "patient" ? "/dashboard" : null);
  if (loading) return <Loading />;
  if (!user) return <Login portal="patient" />;
  if (user.role !== "patient")
    return (
      <main className="fatal">
        <h1>Это кабинет пациента</h1>
        <p>Вы вошли как врач.</p>
        <a className="primary" href="/doctor/">
          Открыть кабинет врача
        </a>
        <Notifications />
        <Account />
      </main>
    );
  const nav = [
    ["today", "Сегодня", CalendarDays],
    ["history", "Дневники", BookOpen],
    ["progress", "Динамика", Activity],
    ["messages", "Переписка", MessageCircle],
    ["support", "Поддержка", HeartHandshake],
    ["audio", "Голос", Mic],
  ] as const;
  return (
    <div className="patient-shell">
      <header className="patient-topbar">
        <Brand />
        <span className="patient-portal-caption">Мой дневник</span>
        <Notifications />
        <Account />
      </header>
      <nav className="patient-nav" aria-label="Основная навигация">
        {nav.map(([key, label, Icon]) => (
          <button
            className={page === key ? "active" : ""}
            key={key}
            onClick={() => {
              setPage(key);
              setSaved("");
            }}
          >
            <Icon size={18} />
            {label}
          </button>
        ))}
      </nav>
      <main className="patient-main">
        <ErrorBox message={error || (data ? queryError : "")} />
        <PatientAlertFeedback />
        {!data ? (
          dashboardLoading ? (
            <Loading />
          ) : (
            <QueryFailure
              error={queryError || "Не удалось загрузить кабинет"}
              retry={reload}
            />
          )
        ) : (
          <>
            {page === "today" ? (
              <>
                <header className="patient-welcome">
                  <div>
                    <p className="date-line">
                      {new Date(data.today + "T12:00:00").toLocaleDateString(
                        "ru-RU",
                        { weekday: "long", day: "numeric", month: "long" },
                      )}
                    </p>
                    <h1>Здравствуйте, {user.name.split(" ")[0]}.</h1>
                    <p>Найдите несколько минут для себя.</p>
                  </div>
                  <div className="welcome-illustration" aria-hidden="true">
                    <span />
                    <Heart size={38} />
                    <i />
                    <b />
                  </div>
                </header>
                {saved ? (
                  <div className="saved-banner" role="status">
                    <Check size={20} />
                    <div>
                      <strong>{saved}</strong>
                      <span>Ответы сохранены и доступны вашему врачу.</span>
                    </div>
                    <button
                      className="text-button"
                      onClick={() => setPage("history")}
                    >
                      Посмотреть
                    </button>
                  </div>
                ) : null}
                <div className="daily-layout">
                  <section className="daily-tasks">
                    <div className="section-heading">
                      <h2>Ваш день</h2>
                      <span className="muted">В вашем темпе</span>
                    </div>
                    {(["csd", "ema"] as const).map((kind) => {
                      const assignment = data.assignments.find(
                        (a) =>
                          a.kind === kind &&
                          (!("status" in a) || a.status === "active") &&
                          a.start_date <= data.today &&
                          a.end_date >= data.today &&
                          Math.round(
                            (Date.parse(data.today + "T00:00:00Z") -
                              Date.parse(a.start_date + "T00:00:00Z")) /
                              86400000,
                          ) %
                            Number(
                              ("interval_days" in a ? a.interval_days : 1) || 1,
                            ) ===
                            0,
                      );
                      const done = data.entries.some(
                        (e) => e.kind === kind && e.local_date === data.today,
                      );
                      return (
                        <article className={`daily-task ${kind}`} key={kind}>
                          <div className="task-time">
                            <span className={`task-icon ${kind}`}>
                              {kind === "csd" ? (
                                <Moon size={23} />
                              ) : (
                                <Sun size={23} />
                              )}
                            </span>
                            <span>{kind === "csd" ? "Утро" : "Вечер"}</span>
                            {assignment ? (
                              <small>
                                <Clock size={13} />
                                {assignment.due_time.slice(0, 5)}
                              </small>
                            ) : null}
                          </div>
                          <div className="task-content">
                            <h3>
                              {kind === "csd"
                                ? "Как вы спали?"
                                : "Как прошёл ваш день?"}
                            </h3>
                            <p>
                              {kind === "csd"
                                ? "Вспомните прошедшую ночь: когда легли, сколько спали и как себя чувствуете."
                                : "Отметьте настроение, энергию и тревогу. Здесь нет правильных или неправильных ответов."}
                            </p>
                            {assignment ? (
                              <small>
                                Назначено до {shortDate(assignment.end_date)}
                              </small>
                            ) : (
                              <small>
                                Пока не назначено. Уточните расписание у врача.
                              </small>
                            )}
                          </div>
                          {done ? (
                            <span className="task-done">
                              <Check size={18} />
                              Заполнено
                            </span>
                          ) : assignment ? (
                            <button
                              className="primary"
                              onClick={() => setForm(kind)}
                            >
                              Заполнить
                              <ArrowRight size={17} />
                            </button>
                          ) : null}
                        </article>
                      );
                    })}
                    <div className="gentle-note">
                      <Heart size={20} />
                      <p>
                        Не нужно оценивать себя. Просто отмечайте то, что
                        замечаете.
                      </p>
                    </div>
                  </section>
                  <aside className="care-card">
                    <span className="care-card-icon">
                      <MessageCircle size={25} />
                    </span>
                    <h2>Вы на связи</h2>
                    {data.care_team.length ? (
                      data.care_team.map((d) => (
                        <div className="doctor-mini" key={d.id}>
                          <strong>{d.name}</strong>
                          <span>Ваш врач</span>
                        </div>
                      ))
                    ) : (
                      <p>Врач пока не назначен.</p>
                    )}
                    <p>
                      Вопросы о дневнике или самочувствии можно обсудить в
                      переписке.
                    </p>
                    <button
                      className="secondary full"
                      onClick={() => setPage("messages")}
                    >
                      Написать врачу
                      <MessageCircle size={16} />
                    </button>
                    <small>Чат не предназначен для экстренной помощи.</small>
                  </aside>
                </div>
                <section className="panel">
                  <h2>Ваши назначения</h2>
                  {data.assignments.length ? (
                    data.assignments.map((a) => (
                      <article
                        className="schedule-row"
                        id={`schedule-${a.id}`}
                        key={a.id}
                      >
                        <div>
                          <strong>
                            {a.kind === "ema" ? "Самочувствие" : "Сон"}
                          </strong>
                          <span>
                            {shortDate(a.start_date)} — {shortDate(a.end_date)},{" "}
                            {a.due_time.slice(0, 5)}; каждые{" "}
                            {a.interval_days ?? 1} дн.
                          </span>
                          <span>
                            {a.status === "paused"
                              ? "Приостановлено"
                              : a.status === "cancelled"
                                ? "Отменено"
                                : "Активно"}
                          </span>
                        </div>
                      </article>
                    ))
                  ) : (
                    <p>Врач пока не назначил дневники.</p>
                  )}
                </section>
                <section className="patient-summary">
                  <div>
                    <span className="soft-icon">
                      <BookOpen size={23} />
                    </span>
                    <div>
                      <h2>Ваши записи складываются в историю</h2>
                      <p>
                        Вы заполнили {data.summary.count} из 3 последних дней
                        самочувствия. Каждый ответ помогает врачу видеть
                        динамику.
                      </p>
                    </div>
                  </div>
                  <button
                    className="text-button"
                    onClick={() => setPage("progress")}
                  >
                    Посмотреть динамику
                    <ArrowRight size={17} />
                  </button>
                </section>
              </>
            ) : null}
            {page === "history" ? (
              <>
                <header className="page-heading">
                  <div>
                    <h1>Ваши дневники</h1>
                    <p className="muted">
                      Сохранённые ответы и возможность исправить запись.
                    </p>
                  </div>
                </header>
                <section className="panel">
                  <History
                    patientID={data.patient.id}
                    entries={data.entries}
                    assignments={data.assignments}
                    editable
                    today={data.today}
                    timezone={data.patient.timezone}
                  />
                </section>
              </>
            ) : null}
            {page === "progress" ? (
              <>
                <header className="page-heading">
                  <div>
                    <h1>Как меняется самочувствие</h1>
                    <p className="muted">
                      Смотрите на историю без спешки и обсудите изменения с
                      врачом.
                    </p>
                  </div>
                </header>
                <section className="panel">
                  <h2>Последние записи EMA</h2>
                  <TrendChart entries={data.entries} />
                </section>
                <EMASummary summary={data.summary} />
                <SleepSummary summary={data.sleep_summary} />
                <section className="panel">
                  <ReviewHistory patientID={user.id} />
                </section>
                <section className="panel">
                  <SleepTrendChart entries={data.entries} />
                </section>
              </>
            ) : null}
            {page === "support" ? (
              <SupportChat
                key={user.id}
                detail={data}
                onDoctorChat={() => setPage("messages")}
                onToday={() => setPage("today")}
                onDiary={(kind) => {
                  const active = data.assignments.some(
                    (assignment) =>
                      assignment.kind === kind &&
                      assignment.status === "active" &&
                      assignment.start_date <= data.today &&
                      assignment.end_date >= data.today &&
                      Math.round(
                        (Date.parse(data.today + "T00:00:00Z") -
                          Date.parse(assignment.start_date + "T00:00:00Z")) /
                          86400000,
                      ) %
                        (assignment.interval_days || 1) ===
                        0,
                  );
                  if (active) setForm(kind);
                  else setPage("today");
                }}
              />
            ) : null}
            {page === "audio" ? (
              <AudioWorkspace key={user.id} patientID={user.id} capture />
            ) : null}
            {page === "messages" ? (
              <>
                <header className="page-heading">
                  <div>
                    <h1>Переписка с врачом</h1>
                    <p className="muted">
                      Место для вопросов и обратной связи.
                    </p>
                  </div>
                </header>
                <section className="panel">
                  {data.care_team.length ? (
                    <Messages patientID={user.id} team={data.care_team} />
                  ) : (
                    <Empty>Врач пока не назначен. Свяжитесь с клиникой.</Empty>
                  )}
                </section>
              </>
            ) : null}
            {form ? (
              <Modal
                title={form === "csd" ? "Дневник сна" : "Дневник самочувствия"}
                onClose={() => setForm(null)}
              >
                <DiaryForm
                  kind={form}
                  today={data.today}
                  timezone={data.patient.timezone}
                  dateRange={data.assignments.find(
                    (a) =>
                      a.kind === form &&
                      a.status === "active" &&
                      a.start_date <= data.today &&
                      a.end_date >= data.today,
                  )}
                  onSaved={() => {
                    setSaved(
                      form === "csd"
                        ? "Дневник сна заполнен"
                        : "Дневник самочувствия заполнен",
                    );
                    setForm(null);
                  }}
                />
              </Modal>
            ) : null}
          </>
        )}
        <footer className="patient-footer">
          <Brand />
          <p>
            Дневник помогает наблюдать за самочувствием и поддерживать связь с
            врачом.
          </p>
          <small>Локальная версия · только синтетические данные</small>
        </footer>
      </main>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      <SessionProvider>
        <PatientApp />
      </SessionProvider>
    </ErrorBoundary>
  </StrictMode>,
);
