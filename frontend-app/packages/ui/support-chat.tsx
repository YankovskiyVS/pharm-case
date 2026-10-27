import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  HeartHandshake,
  Send,
  MessageCircle,
  CalendarDays,
} from "lucide-react";
import { api, APIError, type Detail, type Model } from "../api-client";
import { Empty, ErrorBox, timeLabel } from "./common";
import { confirmDiscard, useDraftGuard } from "./navigation";

export type SupportAgentRun = Model<"SupportAgentRun">;
export type SupportMessage = Model<"SupportMessage">;
export type SupportProvider = Model<"SupportProvider">;
export type SupportHistory = Model<"SupportChatPage">;
type SupportTurn = Model<"SupportChatTurn">;
const path = "/patient/support-chat";
const prompts = [
  "Сегодня мне тяжело. Помоги начать разговор.",
  "Объясни, как заполнять дневник самочувствия EMA.",
  "Объясни, как заполнять дневник сна CSD.",
  "Какие дневники запланированы у меня на сегодня?",
];

export function SupportChat({
  detail,
  onDoctorChat,
  onToday,
  onDiary,
}: {
  detail: Detail;
  onDoctorChat: () => void;
  onToday: () => void;
  onDiary: (kind: "ema" | "csd") => void;
}) {
  const [history, setHistory] = useState<SupportHistory | null>(null);
  const [error, setError] = useState("");
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(true);
  const [moreLoading, setMoreLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [denied, setDenied] = useState(false);
  const [text, setText] = useState("");
  const [consent, setConsent] = useState(false);
  const [version, setVersion] = useState(0);
  const generation = useRef(0);
  const pageCount = useRef(1);
  const sending = useRef(false);
  const pending = useRef({ identity: "", key: crypto.randomUUID() });
  const sendController = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const end = useRef<HTMLDivElement | null>(null);
  const scrollNext = useRef(true);
  useDraftGuard(Boolean(text.trim()), "Сообщение поддержке");

  const clearPrivate = useCallback(() => {
    generation.current++;
    sendController.current?.abort();
    setHistory(null);
    setText("");
    setConsent(false);
    setDenied(true);
    pageCount.current = 1;
  }, []);
  const reload = useCallback(() => setVersion((v) => v + 1), []);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      sendController.current?.abort();
    };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    const run = ++generation.current;
    setLoading(true);
    setError("");
    async function load() {
      let items: SupportMessage[] = [];
      let cursor: string | null = null;
      let last: SupportHistory | null = null;
      for (let i = 0; i < pageCount.current; i++) {
        const query = new URLSearchParams({ limit: "20" });
        if (cursor) query.set("cursor", cursor);
        last = await api<SupportHistory>(
          `${path}?${query}`,
          undefined,
          controller.signal,
        );
        items = [...items, ...last.items];
        cursor = last.next_cursor;
        if (!cursor) break;
      }
      if (controller.signal.aborted || run !== generation.current || !last)
        return;
      setHistory({ ...last, items, next_cursor: cursor });
      setDenied(false);
    }
    void load()
      .catch((e: unknown) => {
        if (controller.signal.aborted || run !== generation.current) return;
        if (e instanceof APIError && (e.status === 401 || e.status === 403))
          clearPrivate();
        setError((e as Error).message);
      })
      .finally(() => {
        if (!controller.signal.aborted && mounted.current) setLoading(false);
      });
    return () => controller.abort();
  }, [version, clearPrivate]);
  useEffect(() => {
    if (history && scrollNext.current) {
      end.current?.scrollIntoView({ block: "nearest" });
      scrollNext.current = false;
    }
  }, [history]);

  async function more() {
    if (!history?.next_cursor || moreLoading) return;
    const run = generation.current;
    setMoreLoading(true);
    setError("");
    try {
      const result = await api<SupportHistory>(
        `${path}?${new URLSearchParams({ limit: "20", cursor: history.next_cursor })}`,
      );
      if (!mounted.current || run !== generation.current) return;
      pageCount.current++;
      setHistory((previous) =>
        previous
          ? { ...result, items: [...previous.items, ...result.items] }
          : result,
      );
    } catch (e) {
      if (!mounted.current || run !== generation.current) return;
      if (e instanceof APIError && (e.status === 401 || e.status === 403))
        clearPrivate();
      setError((e as Error).message);
    } finally {
      if (mounted.current) setMoreLoading(false);
    }
  }
  async function send(event: FormEvent) {
    event.preventDefault();
    if (sending.current || !text.trim() || !history || denied) return;
    if (history.provider.external && !consent) return;
    sending.current = true;
    setBusy(true);
    setFailure("");
    const content = text.trim();
    const identity = JSON.stringify([content, history.provider.mode, consent]);
    if (pending.current.identity !== identity)
      pending.current = { identity, key: crypto.randomUUID() };
    const controller = new AbortController();
    sendController.current = controller;
    try {
      const result = await api<SupportTurn>(
        path,
        {
          text: content,
          request_key: pending.current.key,
          external_consent: consent,
        },
        controller.signal,
        45000,
      );
      if (!mounted.current || controller.signal.aborted) return;
      setHistory((previous) => {
        if (!previous) return previous;
        const all = new Map(previous.items.map((item) => [item.id, item]));
        result.items.forEach((item) => all.set(item.id, item));
        return {
          ...previous,
          provider: result.provider,
          items: [...all.values()],
          total:
            previous.total +
            result.items.filter(
              (item) => !previous.items.some((old) => old.id === item.id),
            ).length,
        };
      });
      setText("");
      pending.current = { identity: "", key: crypto.randomUUID() };
      scrollNext.current = true;
      reload();
    } catch (e) {
      if (!mounted.current || controller.signal.aborted) return;
      if (e instanceof APIError && (e.status === 401 || e.status === 403))
        clearPrivate();
      setFailure((e as Error).message);
    } finally {
      sending.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  const provider = history?.provider;
  const items = [
    ...new Map(history?.items.map((item) => [item.id, item])).values(),
  ].sort(
    (a, b) =>
      a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id),
  );
  const dueToday = detail.assignments.filter(
    (assignment) =>
      assignment.status === "active" &&
      assignment.start_date <= detail.today &&
      assignment.end_date >= detail.today &&
      Math.round(
        (Date.parse(detail.today + "T00:00:00Z") -
          Date.parse(assignment.start_date + "T00:00:00Z")) /
          86400000,
      ) %
        (assignment.interval_days || 1) ===
        0,
  );
  const latestAgent = [...items]
    .reverse()
    .find((item) => item.role === "assistant" && item.agent)?.agent;
  const focusLabels: Record<SupportAgentRun["state"]["focus"], string> = {
    support: "Самочувствие и поддержка",
    ema: "Дневник самочувствия EMA",
    csd: "Дневник сна CSD",
    tasks: "Дневники на сегодня",
    doctor: "Вопрос врачу",
  };
  const nextStepLabels: Record<SupportAgentRun["state"]["next_step"], string> =
    {
      none: "",
      talk: "Можно продолжить с того, что сейчас важно для вас.",
      small_step: "Можно выбрать один небольшой шаг в удобном темпе.",
      doctor_message: "Следующий шаг — сформулировать вопрос врачу.",
      open_diary:
        "Следующий шаг — открыть дневник и заполнить его самостоятельно.",
    };
  const completedTools =
    latestAgent?.tools.filter((tool) => tool.status === "completed") ?? [];
  function continueTalk() {
    const prompt = "Помоги с этим.";
    if (text.trim() && text !== prompt && !confirmDiscard()) return;
    setText(prompt);
  }
  return (
    <>
      <header className="page-heading">
        <div>
          <h1>Поддержка</h1>
          <p className="muted">
            Можно рассказать о своём дне или разобраться с дневниками.
          </p>
        </div>
      </header>
      <div className="support-layout">
        <section className="panel support-chat" aria-label="Чат поддержки">
          <div className="section-heading">
            <h2>
              <HeartHandshake size={21} /> Поддерживающий помощник
            </h2>
            <span className="status-badge">
              {provider?.mode === "demo"
                ? "Демо"
                : provider?.mode === "openai"
                  ? "OpenAI API"
                  : provider?.mode === "ollama"
                    ? "Локальная модель"
                    : provider?.mode === "disabled"
                      ? "Отключён"
                      : "Подключение"}
            </span>
          </div>
          <p className="helper">
            Помощник не ставит диагнозы и не назначает лечение. Ответы не
            являются медицинским заключением. Эта переписка не отправляется
            врачу автоматически.
          </p>
          {provider?.mode === "demo" ? (
            <div className="support-mode" role="note">
              <strong>Демонстрационный режим</strong>
              <p>
                Помощник запоминает тему разговора и выбирает действия по
                демонстрационным правилам: может проверить ваши задачи и
                объяснить дневник. Языковая модель не подключена.
              </p>
            </div>
          ) : null}
          {provider?.agent?.enabled ? (
            <p className="helper support-memory-caption">
              Помощник с памятью диалога: после возвращения можно продолжить ту
              же тему.
            </p>
          ) : null}
          {provider?.mode !== "demo" && provider?.disclaimer ? (
            <p className="helper">{provider.disclaimer}</p>
          ) : null}
          {provider && !provider.ready ? (
            <p className="support-mode" role="status">
              Языковая модель сейчас недоступна. Историю можно читать; новые
              ответы будут содержать сообщение о недоступности.{" "}
              <button className="text-button" onClick={reload}>
                Проверить подключение
              </button>
            </p>
          ) : null}
          {error ? (
            <div>
              <ErrorBox message={error} />
              <button className="secondary" onClick={reload}>
                Повторить загрузку чата
              </button>
            </div>
          ) : null}
          <div className="support-prompts" aria-label="С чего начать разговор">
            {prompts.map((prompt) => (
              <button
                key={prompt}
                className="secondary"
                disabled={busy || denied}
                onClick={() => {
                  if (text.trim() && text !== prompt && !confirmDiscard())
                    return;
                  setText(prompt);
                }}
              >
                {prompt}
              </button>
            ))}
          </div>
          {latestAgent ? (
            <section
              className="support-memory"
              aria-label="Тема и следующий шаг"
            >
              <strong>
                Сейчас обсуждаем: {focusLabels[latestAgent.state.focus]}
              </strong>
              {nextStepLabels[latestAgent.state.next_step] ? (
                <p>{nextStepLabels[latestAgent.state.next_step]}</p>
              ) : null}
              <div className="support-agent-actions">
                {completedTools.some(
                  (tool) => tool.name === "get_today_tasks",
                ) ? (
                  <button className="secondary" onClick={onToday}>
                    Посмотреть задачи на сегодня
                  </button>
                ) : null}
                {completedTools.some(
                  (tool) =>
                    tool.name === "explain_diary" ||
                    (tool.name === "propose_support_step" &&
                      latestAgent.state.next_step === "open_diary"),
                ) &&
                (latestAgent.state.focus === "ema" ||
                  latestAgent.state.focus === "csd") ? (
                  <button
                    className="secondary"
                    onClick={() =>
                      onDiary(latestAgent.state.focus as "ema" | "csd")
                    }
                  >
                    Открыть{" "}
                    {latestAgent.state.focus === "ema"
                      ? "дневник самочувствия"
                      : "дневник сна"}
                  </button>
                ) : null}
                {completedTools.some(
                  (tool) => tool.name === "propose_support_step",
                ) &&
                (latestAgent.state.focus === "doctor" ||
                  latestAgent.state.next_step === "doctor_message") ? (
                  <button className="secondary" onClick={onDoctorChat}>
                    Перейти к переписке с врачом
                  </button>
                ) : null}
                {latestAgent.status === "completed" ? (
                  <button
                    className="text-button"
                    disabled={busy || denied}
                    onClick={continueTalk}
                  >
                    Продолжить разговор
                  </button>
                ) : null}
              </div>
            </section>
          ) : null}
          {history ? (
            <div className="page-controls">
              <span className="muted">
                Показано {items.length} из {history.total}
              </span>
              {history.next_cursor ? (
                <button
                  className="secondary"
                  disabled={moreLoading || busy}
                  onClick={() => void more()}
                >
                  {moreLoading ? "Загружаем…" : "Показать предыдущие сообщения"}
                </button>
              ) : null}
            </div>
          ) : null}
          <div
            className="message-list support-message-list"
            role="log"
            aria-label="Сообщения поддержки"
            aria-live="polite"
            aria-relevant="additions"
          >
            {loading && !history ? (
              <p role="status">Загружаем чат поддержки…</p>
            ) : null}
            {history && !items.length ? (
              <Empty>Как вы сейчас? Можно начать с одного предложения.</Empty>
            ) : null}
            {items.map((message) => (
              <article
                className={`message ${message.role === "user" ? "own" : ""} ${message.safety === "crisis" && message.role === "assistant" ? "support-crisis" : ""}`}
                key={message.id}
              >
                <div>
                  <strong>{message.role === "user" ? "Вы" : "Помощник"}</strong>
                  <time dateTime={message.created_at}>
                    {timeLabel(message.created_at)}
                  </time>
                </div>
                <p>{message.text}</p>
                {message.role === "assistant" ? (
                  <small>
                    {message.provider === "demo"
                      ? message.agent
                        ? "Демо: выбор действий"
                        : "Подготовленный демо-ответ"
                      : message.provider === "safety"
                        ? "Поддержка и обращение к человеку"
                        : message.safety === "provider_unavailable"
                          ? "Ответ модели не получен"
                          : `Модель: ${message.model}`}
                  </small>
                ) : null}
                {message.role === "assistant" &&
                message.agent &&
                message.agent.tools.length > 0 ? (
                  <details className="support-evidence">
                    <summary>На что опирается ответ</summary>
                    <ul>
                      {message.agent.tools.map((tool, index) => (
                        <li key={`${tool.name}-${index}`}>
                          <strong>
                            {tool.name === "get_today_tasks"
                              ? "Ваши задачи и заполнение дневников на сегодня"
                              : tool.name === "explain_diary"
                                ? "Пояснение к дневнику"
                                : tool.name === "propose_support_step"
                                  ? "Небольшой шаг поддержки"
                                  : "Недоступное действие"}
                          </strong>
                          <span>
                            {tool.status === "completed"
                              ? tool.summary
                              : "Источник не использован: действие недоступно."}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </details>
                ) : null}
              </article>
            ))}
            {busy ? <p role="status">Помощник готовит ответ…</p> : null}
            <div ref={end} />
          </div>
          <ErrorBox message={failure} />
          <form className="support-form" onSubmit={send}>
            {provider?.external ? (
              <label className="checkbox support-consent">
                <input
                  type="checkbox"
                  checked={consent}
                  disabled={busy}
                  onChange={(event) => setConsent(event.target.checked)}
                />
                <span>
                  Разрешаю передать мой текст и последние сообщения этого чата и
                  необходимые сведения о моих задачах и заполнении дневников на
                  сегодня внешнему провайдеру{" "}
                  {provider.mode === "openai" ? "OpenAI" : "модели"} для ответа.
                  Врачебная история и аудиозаписи не передаются.
                </span>
              </label>
            ) : null}
            <label>
              Сообщение помощнику
              <textarea
                value={text}
                onChange={(event) => setText(event.target.value)}
                maxLength={2000}
                rows={3}
                disabled={busy || denied}
                placeholder="О чём хочется поговорить?"
                required
              />
            </label>
            <div className="support-compose-actions">
              <small className="muted">{Array.from(text).length} / 2000</small>
              <button
                className="primary"
                disabled={
                  busy ||
                  !text.trim() ||
                  !history ||
                  denied ||
                  provider?.mode === "disabled" ||
                  Boolean(provider?.external && !consent)
                }
              >
                {busy ? "Ожидаем ответ…" : "Отправить помощнику"}
                <Send size={17} />
              </button>
            </div>
          </form>
        </section>
        <aside className="support-aside">
          <section className="panel support-human">
            <MessageCircle size={24} />
            <h2>Рядом есть люди</h2>
            <p>
              Вопросы о лечении и изменениях самочувствия обсудите со своим
              врачом.
            </p>
            <button className="secondary full" onClick={onDoctorChat}>
              Написать врачу
            </button>
            <p className="helper">
              Врач не читает этот чат. В переписке он ответит, когда будет
              доступен.
            </p>
            <p className="support-urgent">
              Если есть непосредственная опасность для вас или другого человека,
              обратитесь за экстренной помощью и попросите близкого человека
              побыть рядом. Не ждите ответа в чате.
            </p>
          </section>
          <section className="panel">
            <h2>
              <CalendarDays size={20} /> Дневники на сегодня
            </h2>
            <p className="helper">
              По расписанию врача, {detail.patient.timezone}.
            </p>
            {dueToday.length ? (
              <ul className="support-reminders">
                {dueToday.map((assignment) => (
                  <li key={assignment.id}>
                    <strong>
                      {assignment.kind === "csd"
                        ? "Дневник сна CSD"
                        : "Дневник самочувствия EMA"}
                    </strong>
                    <span>
                      {assignment.due_time.slice(0, 5)} ·{" "}
                      {detail.entries.some(
                        (entry) =>
                          entry.kind === assignment.kind &&
                          entry.local_date === detail.today,
                      )
                        ? "Заполнен"
                        : "Ожидает заполнения"}
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <p>На сегодня активных назначений нет.</p>
            )}
            <button className="text-button" onClick={onToday}>
              Открыть дневники
            </button>
          </section>
        </aside>
      </div>
    </>
  );
}
