import type { Alert } from "../api-client";
import { useQuery } from "./session";
import { useUrlState, navigate } from "./navigation";
import { ErrorBox, timeLabel } from "./common";
export function PatientAlertFeedback() {
  const [target] = useUrlState("target", ""),
    [kind] = useUrlState("target_type", "");
  const { data, error } = useQuery<Alert>(
    target && kind === "alert" ? `/alerts/${target}` : null,
  );
  if (kind !== "alert" || !target) return null;
  return (
    <section className="panel">
      <h2>Ответ по сохранённому сигналу</h2>
      <ErrorBox message={error} />
      {data ? (
        <>
          <p>Сигнал сохранён {timeLabel(data.created_at)}.</p>
          <p>
            {data.status === "new"
              ? "Подтверждения ознакомления врача пока нет."
              : data.status === "ack"
                ? "Врач подтвердил ознакомление."
                : "Врач закрыл сигнал и зафиксировал действие."}
          </p>
          <p className="helper">
            Статус не заменяет общение с врачом. При непосредственной опасности
            обратитесь за экстренной помощью.
          </p>
        </>
      ) : !error ? (
        <p role="status">Загружаем статус…</p>
      ) : null}
      <button
        className="text-button"
        onClick={() => navigate({ target: null, target_type: null })}
      >
        Закрыть
      </button>
    </section>
  );
}
