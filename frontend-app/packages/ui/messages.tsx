import { useRef, useState, useEffect, type FormEvent } from "react";
import { Send, MessageCircle } from "lucide-react";
import { api, refreshData, type Message, type Model } from "../api-client";
import { useSession } from "./session";
import { usePages, PageControls, QueryFailure } from "./pagination";
import { useDraftGuard, confirmDiscard, useUrlState } from "./navigation";
import { Empty, ErrorBox, timeLabel } from "./common";
export function Messages({
  patientID,
  team,
}: {
  patientID: string;
  team: Model<"CareMember">[];
}) {
  const { user } = useSession();

  const [text, setText] = useState("");
  const [doctor, setDoctor] = useUrlState("doctor", team[0]?.id ?? "");
  const recipient = user?.role === "doctor" ? user.id : doctor;
  const page = usePages<Message>(
    recipient
      ? `/patients/${patientID}/messages?page=true&doctor_id=${recipient}`
      : null,
    true,
  );
  const data = page.items,
    error = page.error,
    loading = page.loading;
  useDraftGuard(Boolean(text.trim()), "Сообщение");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const key = useRef(crypto.randomUUID());
  const previous = useRef("");
  const readIds = useRef(new Set<string>());
  const [, setReadVersion] = useState(0);
  const incoming = page.items
    .filter(
      (m) =>
        m.sender_id !== user?.id &&
        !(m as Message & { is_read?: boolean }).is_read,
    )
    .map((m) => m.id)
    .join(",");
  useEffect(() => {
    const ids = incoming
      .split(",")
      .filter((id) => id && !readIds.current.has(id));
    if (!ids.length || !recipient) return;
    void api(`/patients/${patientID}/messages/read`, {
      doctor_id: recipient,
      ids,
    })
      .then(() => {
        ids.forEach((id) => readIds.current.add(id));
        setReadVersion((v) => v + 1);
      })
      .catch((e) => setFailure((e as Error).message));
  }, [incoming, patientID, recipient]);
  async function send(event: FormEvent) {
    event.preventDefault();
    if (!text.trim()) return;
    setBusy(true);
    setFailure("");
    const identity = JSON.stringify([recipient, text]);
    if (previous.current !== identity) {
      key.current = crypto.randomUUID();
      previous.current = identity;
    }
    try {
      await api(`/patients/${patientID}/messages`, {
        body: text,
        request_key: key.current,
        doctor_id: recipient,
      });
      setText("");
      previous.current = "";
      key.current = crypto.randomUUID();
      refreshData();
    } catch (e) {
      setFailure((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="messages-panel">
      <div className="section-heading">
        <h2>
          <MessageCircle size={20} />
          Переписка с {user?.role === "doctor" ? "пациентом" : "врачом"}
        </h2>
        <span className="muted">Личный канал связи</span>
      </div>
      <p className="helper">
        Чат не предназначен для экстренной помощи. Врач ответит, когда будет
        доступен.
      </p>
      {user?.role === "patient" && team.length > 1 ? (
        <label>
          Получатель
          <select
            value={doctor}
            onChange={(e) => {
              if (!confirmDiscard()) return;
              setDoctor(e.target.value);
              setFailure("");
              setText("");
              key.current = crypto.randomUUID();
              previous.current = "";
            }}
          >
            {team.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
      ) : null}

      <QueryFailure error={error} retry={page.reload} />
      <ErrorBox message={failure} />
      <PageControls
        total={page.total}
        shown={page.items.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
      <p className="helper">
        Непрочитанных в диалоге:{" "}
        {Math.max(
          0,
          page.unread -
            page.items.filter(
              (m) =>
                m.sender_id !== user?.id &&
                !(m as Message & { is_read?: boolean }).is_read &&
                readIds.current.has(m.id),
            ).length,
        )}
      </p>
      <div className="message-list" aria-live="polite">
        {loading ? (
          <p role="status">Загружаем переписку…</p>
        ) : !data?.length ? (
          <Empty>Начните разговор: отправьте первое сообщение.</Empty>
        ) : (
          data.map((m) => (
            <article
              className={`message ${m.sender_id === user?.id ? "own" : ""}`}
              key={m.id}
            >
              <div>
                <strong>{m.sender_name}</strong>
                <time>{timeLabel(m.created_at)}</time>
              </div>
              <p>{m.body}</p>
              {m.sender_id === user?.id ? (
                <small>
                  {(m as Message & { read_at?: string | null }).read_at
                    ? "Прочитано адресатом"
                    : "Отправлено"}
                </small>
              ) : null}
            </article>
          ))
        )}
      </div>
      <form className="message-form" onSubmit={send}>
        <label className="message-input">
          Сообщение
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            maxLength={4000}
            rows={3}
            placeholder="Напишите сообщение…"
            required
          />
        </label>
        <button
          className="primary"
          disabled={
            busy || !text.trim() || (user?.role === "patient" && !doctor)
          }
        >
          {busy ? "Отправляем…" : "Отправить"}
          <Send size={17} />
        </button>
      </form>
    </section>
  );
}
