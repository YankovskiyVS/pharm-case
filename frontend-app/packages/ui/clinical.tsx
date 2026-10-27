import { useEffect, useRef, useState, type FormEvent } from "react";
import { APIError, api, refreshData, type Model } from "../api-client";
import { useQuery, useSession } from "./session";
import { ErrorBox, Loading, Notice, timeLabel } from "./common";
import { confirmDiscard, useDraftGuard } from "./navigation";
import { usePages, PageControls, QueryFailure } from "./pagination";

export type ExternalScale = Model<"ExternalScale">;
export type ClinicalContext = Model<"ClinicalContext">;
export type ContextVersion = Model<"ClinicalContextVersion">;
type ContextDraft = Required<Omit<ClinicalContext, "external_scales">> & {
  external_scales: Array<Omit<ExternalScale, "total"> & { total: number | "" }>;
};
type LatestContext = ContextVersion;
type EvidenceArticle = {
  pmid: string;
  title: string;
  abstract: string;
  journal: string;
  published_at: string;
  url: string;
  article_types: string[];
};
type EvidenceCitation = { text: string; pmids: string[] };
type EvidenceMedication = {
  generic_name: string;
  role: string;
  use: string;
  monitoring: string;
  contraindications: string;
  pmids: string[];
};
type EvidenceReview = {
  id: string;
  status:
    | "queued"
    | "searching"
    | "analyzing"
    | "ready"
    | "failed"
    | "model_unavailable";
  sources: EvidenceArticle[];
  provider?: string;
  model?: string;
  prompt_version: string;
  error_code?: string;
  created_at: string;
  completed_at?: string;
  result?: {
    summary: string;
    summary_pmids: string[];
    medications: EvidenceMedication[];
    non_drug_recommendations: EvidenceCitation[];
    monitoring: EvidenceCitation[];
    uncertainties: string[];
  };
};
const fields = {
  medical_history: "Соматический анамнез",
  psychiatric_history: "Психиатрический анамнез",
  diagnoses: "Диагнозы (запись врача)",
  medications: "Препараты и приём / клинические рекомендации",
  confounders: "Факторы, влияющие на голос и самочувствие",
} as const;
const instruments = {
  PHQ9: { name: "PHQ-9", max: 27 },
  GAD7: { name: "GAD-7", max: 21 },
  MADRS: { name: "MADRS (врачебная)", max: 60 },
  HDRS17: { name: "HDRS-17", max: 52 },
};
const emptyContext = (): ContextDraft => ({
  medical_history: "",
  psychiatric_history: "",
  diagnoses: "",
  medications: "",
  confounders: "",
  external_scales: [],
});
export function useClinicalWrite() {
  const keys = useRef(new Map<string, string>());
  return async function write<T>(
    path: string,
    body: Record<string, unknown>,
  ): Promise<T> {
    const signature = JSON.stringify([path, body]);
    let requestKey = keys.current.get(signature);
    if (!requestKey) {
      requestKey = crypto.randomUUID();
      keys.current.set(signature, requestKey);
    }
    try {
      const result = await api<T>(path, { ...body, request_key: requestKey });
      keys.current.delete(signature);
      return result;
    } catch (error) {
      if (
        error instanceof APIError &&
        (error.status === 401 || error.status === 403)
      )
        refreshData();
      throw error;
    }
  };
}
export function ContextFacts({ context }: { context: ClinicalContext | null }) {
  if (!context)
    return <p className="helper">Клинический контекст ещё не внесён.</p>;
  return (
    <div className="clinical-facts">
      {Object.entries(fields).map(([key, label]) => (
        <section key={key}>
          <h4>{label}</h4>
          <p className="preserve-lines">
            {context[key as keyof typeof fields] || "Не указано"}
          </p>
        </section>
      ))}
      <section>
        <h4>Внешние оценки по шкалам</h4>
        {context.external_scales?.length ? (
          <div className="table-scroll">
            <table>
              <caption>Итоги внешнего обследования, внесённые врачом</caption>
              <thead>
                <tr>
                  <th>Шкала</th>
                  <th>Сумма</th>
                  <th>Дата оценки</th>
                  <th>Источник</th>
                  <th>Примечание</th>
                </tr>
              </thead>
              <tbody>
                {context.external_scales.map((score, i) => (
                  <tr key={i}>
                    <td>{instruments[score.scale]?.name ?? score.scale}</td>
                    <td>{score.total}</td>
                    <td>{score.assessed_on}</td>
                    <td>{score.source}</td>
                    <td>{score.notes || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p>Внешние оценки не указаны.</p>
        )}
      </section>
    </div>
  );
}
function ContextHistory({ patientID }: { patientID: string }) {
  const page = usePages<ContextVersion>(
    `/patients/${patientID}/clinical-context/history`,
  );
  return (
    <section className="panel">
      <h3>История клинического контекста</h3>
      <QueryFailure error={page.error} retry={page.reload} />
      {page.loading ? (
        <Loading />
      ) : page.items.length ? (
        page.items.map((item) => (
          <details className="clinical-version" key={item.id}>
            <summary>
              Версия {item.version} · {item.author_name} ·{" "}
              {item.created_at ? timeLabel(item.created_at) : "Дата не указана"}
            </summary>
            <ContextFacts context={item.context} />
            <p className="helper">Идентификатор версии: {item.id}</p>
          </details>
        ))
      ) : !page.error ? (
        <p>Сохранённых версий пока нет.</p>
      ) : null}
      <PageControls
        total={page.total}
        shown={page.items.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
    </section>
  );
}
function EvidenceResearch({
  patientID,
  onRecommendation,
}: {
  patientID: string;
  onRecommendation: (reviewID: string, text: string) => void;
}) {
  const query = useQuery<EvidenceReview[]>(
    `/patients/${patientID}/clinical-evidence-reviews`,
  );
  const [retrying, setRetrying] = useState(false);
  const [error, setError] = useState("");
  const review = query.data?.[0];
  useEffect(() => {
    if (review?.status !== "ready" || !review.result) return;
    const citations = (ids: string[]) =>
      ids.map((id) => `PMID ${id}`).join(", ");
    const sections = [
      `Доказательный обзор: ${review.result.summary} (${citations(review.result.summary_pmids)})`,
      review.result.medications.length
        ? `Препараты и применение:\n${review.result.medications.map((item) => `• ${item.generic_name}: ${item.role}. ${item.use}. Мониторинг: ${item.monitoring}. Ограничения: ${item.contraindications}. (${citations(item.pmids)})`).join("\n")}`
        : "Препараты: в найденных источниках недостаточно данных для рекомендации.",
      review.result.non_drug_recommendations.length
        ? `Немедикаментозные рекомендации:\n${review.result.non_drug_recommendations.map((item) => `• ${item.text} (${citations(item.pmids)})`).join("\n")}`
        : "",
      review.result.monitoring.length
        ? `Мониторинг:\n${review.result.monitoring.map((item) => `• ${item.text} (${citations(item.pmids)})`).join("\n")}`
        : "",
      review.result.uncertainties.length
        ? `Ограничения доказательств:\n${review.result.uncertainties.map((item) => `• ${item}`).join("\n")}`
        : "",
      "Черновик сформирован автоматически по PubMed. Требуется проверка и подтверждение врачом.",
    ]
      .filter(Boolean)
      .join("\n\n");
    onRecommendation(review.id, Array.from(sections).slice(0, 5000).join(""));
  }, [review?.id, review?.status, review?.result, onRecommendation]);
  const pmidLinks = (ids: string[]) =>
    ids.map((id, i) => (
      <span key={id}>
        {i ? ", " : ""}
        <a
          href={`https://pubmed.ncbi.nlm.nih.gov/${id}/`}
          target="_blank"
          rel="noreferrer"
        >
          PMID {id}
        </a>
      </span>
    ));
  async function retry() {
    if (!review) return;
    setRetrying(true);
    setError("");
    try {
      await api(
        `/patients/${patientID}/clinical-evidence-reviews/${review.id}/retry`,
        {},
      );
      refreshData();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setRetrying(false);
    }
  }
  return (
    <section className="panel evidence-review">
      <h2>Доказательный ресерч по диагнозам</h2>
      <p className="helper">
        После сохранения диагнозов PubMed подбирает рекомендации и исследования,
        а OpenAI формирует источник-связанный черновик. Это не назначение: врач
        проверяет применимость, полные тексты и актуальные инструкции.
      </p>
      <QueryFailure error={query.error} retry={query.reload} />
      <ErrorBox message={error} />
      {query.loading && !query.data ? (
        <Loading />
      ) : !review ? (
        <p>
          Сохраните клинический контекст с диагнозом, чтобы запустить ресерч.
        </p>
      ) : review.status !== "ready" || !review.result ? (
        <div>
          <p>
            <strong>Статус:</strong>{" "}
            {
              {
                queued: "в очереди",
                searching: "поиск в PubMed",
                analyzing: "анализ источников",
                failed: "ошибка",
                model_unavailable:
                  "источники найдены, анализ OpenAI не настроен",
                ready: "готово",
              }[review.status]
            }
          </p>
          {review.sources?.length ? (
            <p>Найдено источников: {review.sources.length}.</p>
          ) : null}
          {review.error_code === "provider_quota_exhausted" ? (
            <p className="error-box">
              Баланс OpenAI API исчерпан. Пополните баланс проекта и повторите
              ресерч.
            </p>
          ) : null}
          {review.status === "failed" ||
          review.status === "model_unavailable" ? (
            <button
              className="secondary"
              disabled={retrying}
              onClick={() => void retry()}
            >
              {retrying ? "Повтор…" : "Повторить ресерч"}
            </button>
          ) : null}
        </div>
      ) : (
        <>
          <p className="evidence-warning">
            <strong>Черновик для врача.</strong> Не является автоматически
            подтверждённым диагнозом или назначением.
          </p>
          {review.provider === "demo" ? (
            <p className="error-box">
              <strong>Демонстрационный режим.</strong> Текст синтетический и
              показывает интерфейс; для клинического использования требуется
              OpenAI-анализ и проверка врачом.
            </p>
          ) : null}
          <p className="preserve-lines">{review.result.summary}</p>
          <p className="evidence-pmids">
            {pmidLinks(review.result.summary_pmids)}
          </p>
          <h3>Препараты и применение в источниках</h3>
          {review.result.medications.length ? (
            review.result.medications.map((m) => (
              <article
                className="evidence-item"
                key={`${m.generic_name}-${m.role}`}
              >
                <h4>{m.generic_name}</h4>
                <p>{m.role}</p>
                <p>
                  <strong>Применение:</strong> {m.use}
                </p>
                <p>
                  <strong>Мониторинг:</strong> {m.monitoring}
                </p>
                <p>
                  <strong>Ограничения:</strong> {m.contraindications}
                </p>
                <p className="evidence-pmids">{pmidLinks(m.pmids)}</p>
              </article>
            ))
          ) : (
            <p>
              Подтверждённых сведений о препаратах в найденных источниках
              недостаточно.
            </p>
          )}
          <h3>Немедикаментозные рекомендации</h3>
          <ul>
            {review.result.non_drug_recommendations.map((x, i) => (
              <li key={i}>
                {x.text}{" "}
                <span className="evidence-pmids">{pmidLinks(x.pmids)}</span>
              </li>
            ))}
          </ul>
          <h3>Мониторинг</h3>
          <ul>
            {review.result.monitoring.map((x, i) => (
              <li key={i}>
                {x.text}{" "}
                <span className="evidence-pmids">{pmidLinks(x.pmids)}</span>
              </li>
            ))}
          </ul>
          {review.result.uncertainties.length ? (
            <>
              <h3>Неопределённости</h3>
              <ul>
                {review.result.uncertainties.map((x, i) => (
                  <li key={i}>{x}</li>
                ))}
              </ul>
            </>
          ) : null}
          <details>
            <summary>Источники PubMed ({review.sources.length})</summary>
            {review.sources.map((a) => (
              <article className="evidence-source" key={a.pmid}>
                <h4>
                  <a href={a.url} target="_blank" rel="noreferrer">
                    {a.title}
                  </a>
                </h4>
                <p>
                  {a.journal} · {a.published_at || "дата не указана"} · PMID{" "}
                  {a.pmid}
                </p>
                <p>{a.abstract || "Abstract отсутствует."}</p>
              </article>
            ))}
          </details>
          <p className="helper">
            Модель: {review.model || "не указана"} · версия анализа:{" "}
            {review.prompt_version}
          </p>
        </>
      )}
    </section>
  );
}
export function ClinicalWorkspace({
  patientID,
  today,
}: {
  patientID: string;
  today: string;
}) {
  const { user } = useSession();
  return user?.role === "doctor" ? (
    <DoctorClinicalWorkspace
      key={patientID}
      patientID={patientID}
      today={today}
    />
  ) : null;
}
function DoctorClinicalWorkspace({
  patientID,
  today,
}: {
  patientID: string;
  today: string;
}) {
  const path = `/patients/${patientID}/clinical-context`;
  const query = useQuery<LatestContext>(path);
  const [draft, setDraft] = useState<ContextDraft | null>(null);
  const [base, setBase] = useState(0);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [notice, setNotice] = useState("");
  const write = useClinicalWrite();
  const appliedResearch = useRef("");
  useDraftGuard(dirty, "Клинический контекст");
  function editLatest() {
    if (!query.data || !confirmDiscard()) return;
    setDraft({
      ...emptyContext(),
      ...query.data.context,
      external_scales: structuredClone(
        query.data.context?.external_scales ?? [],
      ),
    });
    setBase(query.data.version);
    setDirty(false);
    setConflict(false);
    setError("");
    setNotice("");
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft) return;
    setBusy(true);
    setError("");
    setConflict(false);
    try {
      const saved = await write<ContextVersion>(path, {
        base_version: base,
        context: {
          ...draft,
          external_scales: draft.external_scales.map((s) => ({
            ...s,
            total: Number(s.total),
          })),
        },
      });
      const awaitingResearch =
        Boolean(draft.diagnoses.trim()) && !draft.medications.trim();
      setDraft(awaitingResearch ? draft : null);
      setBase(saved.version);
      setDirty(false);
      setNotice(
        awaitingResearch
          ? "Диагноз сохранён. Выполняется поиск клинических рекомендаций; поле заполнится автоматически."
          : "Клинический контекст сохранён новой версией.",
      );
      refreshData();
    } catch (e) {
      setError((e as Error).message);
      if (e instanceof APIError && e.status === 409) {
        setConflict(true);
        query.reload();
      }
    } finally {
      setBusy(false);
    }
  }
  function change(value: ContextDraft) {
    setDraft(value);
    setDirty(true);
    setNotice("");
  }
  function applyRecommendation(reviewID: string, value: string) {
    if (appliedResearch.current === reviewID) return;
    setDraft((current) => {
      if (!current || current.medications.trim()) return current;
      appliedResearch.current = reviewID;
      setDirty(true);
      setNotice(
        "Клинические рекомендации добавлены автоматически. Проверьте их и сохраните новую версию контекста.",
      );
      return { ...current, medications: value };
    });
  }
  return (
    <div className="clinical-workspace">
      <section className="panel">
        <h2>Клинический контекст</h2>
        <p className="helper">
          Доступен лечащим врачам. Каждое сохранение создаёт неизменяемую версию
          с автором и датой. Пациент не видит эти записи.
        </p>
        <QueryFailure error={query.error} retry={query.reload} />
        <ErrorBox message={error} />
        {notice ? <Notice>{notice}</Notice> : null}
        {!query.data ? (
          query.loading ? (
            <Loading />
          ) : null
        ) : (
          <>
            <p>
              Текущая версия: {query.data.version || "ещё нет"}
              {query.data.author_name ? ` · ${query.data.author_name}` : ""}
              {query.data.created_at
                ? ` · ${timeLabel(query.data.created_at)}`
                : ""}
            </p>
            {!draft ? (
              <>
                <ContextFacts context={query.data.context} />
                <button className="primary" onClick={editLatest}>
                  Редактировать клинический контекст
                </button>
              </>
            ) : (
              <form className="clinical-editor" onSubmit={save}>
                <p>Редактируется версия {base || "новая запись"}.</p>
                {query.data.version !== base || conflict ? (
                  <div className="error-box" role="alert">
                    <span>
                      Контекст изменился. Черновик сохранён на странице.
                      Загрузите актуальную версию перед повторным
                      редактированием.
                    </span>
                    <button
                      className="text-button"
                      type="button"
                      disabled={
                        busy ||
                        query.loading ||
                        (conflict && query.data.version === base)
                      }
                      onClick={editLatest}
                    >
                      Загрузить актуальную версию
                    </button>
                  </div>
                ) : null}
                {Object.entries(fields).map(([key, label]) => (
                  <label key={key}>
                    {label}
                    <textarea
                      maxLength={5000}
                      rows={4}
                      required={key === "diagnoses"}
                      placeholder={
                        key === "diagnoses"
                          ? "Введите поставленный диагноз"
                          : key === "medications"
                            ? "Заполнится автоматически после доказательного ресерча"
                            : undefined
                      }
                      value={draft[key as keyof typeof fields]}
                      disabled={busy}
                      onChange={(e) =>
                        change({ ...draft, [key]: e.target.value })
                      }
                    />
                  </label>
                ))}
                <h3>Результаты внешних обследований</h3>
                <p className="helper">
                  Только сумма из выполненной вне приложения оценки с датой и
                  источником. Полные анкеты здесь не заполняются. MADRS —
                  врачебная шкала, отличная от MADRS-S. Категории тяжести и
                  диагноз автоматически не вычисляются.
                </p>
                {draft.external_scales.map((score, i) => (
                  <fieldset key={i} className="clinical-scale">
                    <legend>Внешняя оценка {i + 1}</legend>
                    <div className="clinical-scale-grid">
                      <label>
                        Шкала
                        <select
                          value={score.scale}
                          disabled={busy}
                          onChange={(e) =>
                            change({
                              ...draft,
                              external_scales: draft.external_scales.map(
                                (s, n) =>
                                  n === i
                                    ? {
                                        ...s,
                                        scale: e.target
                                          .value as ExternalScale["scale"],
                                      }
                                    : s,
                              ),
                            })
                          }
                        >
                          {Object.entries(instruments).map(
                            ([key, instrument]) => (
                              <option key={key} value={key}>
                                {instrument.name}
                              </option>
                            ),
                          )}
                        </select>
                      </label>
                      <label>
                        Сумма (0–{instruments[score.scale].max})
                        <input
                          type="number"
                          min={0}
                          max={instruments[score.scale].max}
                          step={1}
                          value={score.total}
                          required
                          disabled={busy}
                          onChange={(e) =>
                            change({
                              ...draft,
                              external_scales: draft.external_scales.map(
                                (s, n) =>
                                  n === i
                                    ? {
                                        ...s,
                                        total:
                                          e.target.value === ""
                                            ? ""
                                            : Number(e.target.value),
                                      }
                                    : s,
                              ),
                            })
                          }
                        />
                      </label>
                      <label>
                        Дата оценки
                        <input
                          type="date"
                          value={score.assessed_on}
                          required
                          disabled={busy}
                          onChange={(e) =>
                            change({
                              ...draft,
                              external_scales: draft.external_scales.map(
                                (s, n) =>
                                  n === i
                                    ? { ...s, assessed_on: e.target.value }
                                    : s,
                              ),
                            })
                          }
                        />
                      </label>
                    </div>
                    <label>
                      Источник внешней оценки
                      <input
                        value={score.source}
                        maxLength={1000}
                        required
                        disabled={busy}
                        placeholder="Кем и по какому документу проведена оценка"
                        onChange={(e) =>
                          change({
                            ...draft,
                            external_scales: draft.external_scales.map(
                              (s, n) =>
                                n === i ? { ...s, source: e.target.value } : s,
                            ),
                          })
                        }
                      />
                    </label>
                    <label>
                      Примечание к оценке
                      <textarea
                        value={score.notes ?? ""}
                        maxLength={2000}
                        disabled={busy}
                        onChange={(e) =>
                          change({
                            ...draft,
                            external_scales: draft.external_scales.map(
                              (s, n) =>
                                n === i ? { ...s, notes: e.target.value } : s,
                            ),
                          })
                        }
                      />
                    </label>
                    <button
                      type="button"
                      className="text-button"
                      disabled={busy}
                      onClick={() =>
                        change({
                          ...draft,
                          external_scales: draft.external_scales.filter(
                            (_, n) => n !== i,
                          ),
                        })
                      }
                    >
                      Удалить внешнюю оценку {i + 1}
                    </button>
                  </fieldset>
                ))}
                <button
                  type="button"
                  className="secondary"
                  disabled={busy || draft.external_scales.length >= 30}
                  onClick={() =>
                    change({
                      ...draft,
                      external_scales: [
                        ...draft.external_scales,
                        {
                          scale: "PHQ9",
                          total: "",
                          assessed_on: today,
                          source: "",
                          notes: "",
                        },
                      ],
                    })
                  }
                >
                  Добавить внешнюю оценку
                </button>
                <div className="button-row">
                  <button
                    className="primary"
                    disabled={busy || conflict || query.data.version !== base}
                  >
                    {busy ? "Сохраняем…" : "Сохранить новую версию контекста"}
                  </button>
                  <button
                    type="button"
                    className="text-button"
                    disabled={busy}
                    onClick={() => {
                      if (confirmDiscard()) {
                        setDraft(null);
                        setDirty(false);
                      }
                    }}
                  >
                    Отменить редактирование
                  </button>
                </div>
              </form>
            )}
          </>
        )}
      </section>
      <EvidenceResearch
        patientID={patientID}
        onRecommendation={applyRecommendation}
      />
      <ContextHistory patientID={patientID} />
    </div>
  );
}
