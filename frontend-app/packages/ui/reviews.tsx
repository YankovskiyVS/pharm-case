import { useState } from "react";
import { api, type Model, type Entry } from "../api-client";
import { ErrorBox, Modal, shortDate, timeLabel } from "./common";
import { Answers } from "./history";
import { useQuery } from "./session";
import { useUrlState } from "./navigation";
import { usePages, PageControls, QueryFailure } from "./pagination";
type Review = Model<"Review"> & {
  period_start?: string;
  period_end?: string;
  source_entry_ids?: string[];
};
export function ReviewHistory({ patientID }: { patientID: string }) {
  const page = usePages<Review>(`/patients/${patientID}/reviews`);
  const [target] = useUrlState("target", ""),
    [targetType] = useUrlState("target_type", "");
  const direct = useQuery<Review>(
    target && targetType === "review"
      ? `/patients/${patientID}/reviews/${target}`
      : null,
  );
  const reviews = direct.data
    ? [direct.data, ...page.items.filter((r) => r.id !== direct.data?.id)]
    : page.items;

  const [source, setSource] = useState<Entry[] | null>(null);
  const [error, setError] = useState("");
  async function open(id: string) {
    try {
      setSource(
        await api<Entry[]>(`/patients/${patientID}/diaries/${id}/revisions`),
      );
    } catch (e) {
      setError((e as Error).message);
    }
  }
  return (
    <>
      <h3>История осмотров</h3>
      <QueryFailure error={page.error || direct.error} retry={page.reload} />
      <ErrorBox message={error} />
      {reviews.map((r) => (
        <article className="review-row" key={r.id}>
          <div>
            <strong>
              {r.action === "reviewed"
                ? "Ознакомлен"
                : "Записано намерение связаться"}
            </strong>
            <small>
              {r.doctor_name}, {timeLabel(r.created_at)}
            </small>
            {r.period_start && r.period_end ? (
              <p>
                Просмотренный период: {shortDate(r.period_start)} —{" "}
                {shortDate(r.period_end)}
              </p>
            ) : null}
            {r.note ? <p>{r.note}</p> : null}
            <details>
              <summary>
                Записи, зафиксированные при осмотре (
                {r.source_entry_ids?.length ?? 0})
              </summary>
              {r.source_entry_ids?.map((id) => (
                <button
                  className="text-button"
                  key={id}
                  onClick={() => void open(id)}
                >
                  Открыть исходную запись {id.slice(0, 8)}
                </button>
              ))}
            </details>
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
      {source ? (
        <Modal
          title="Зафиксированная версия записи"
          onClose={() => setSource(null)}
        >
          <div>
            {source.map((entry) => (
              <article className="history-row" key={entry.id}>
                <div>
                  <strong>
                    {entry.kind === "ema" ? "Самочувствие" : "Сон"} —{" "}
                    {shortDate(entry.local_date)}
                  </strong>
                  <Answers entry={entry} />
                </div>
              </article>
            ))}
          </div>
        </Modal>
      ) : null}
    </>
  );
}
