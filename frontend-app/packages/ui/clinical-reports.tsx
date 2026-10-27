import { useState, type FormEvent } from "react";
import type { Model } from "../api-client";
import { refreshData } from "../api-client";
import { useQuery, useSession } from "./session";
import { ErrorBox, Loading, Notice, timeLabel } from "./common";
import { PageControls, QueryFailure, usePages } from "./pagination";
import { ContextFacts, useClinicalWrite } from "./clinical";
import { Answers } from "./history";
import { EMASummary, SleepSummary } from "./summaries";
import { confirmDiscard, useDraftGuard } from "./navigation";
import { metricLabels } from "./audio-metrics";

type VoiceStatus = Model<"VoiceFMStatus">;
type SourceSnapshot = Model<"ClinicalReportSnapshot">;
export type ClinicalReport = Model<"ClinicalReport">;
const modelStates = {
  queued: "В очереди обработки",
  processing: "Модель обрабатывает запись",
  ready: "Результат модели сохранён",
  model_unavailable: "Модель недоступна",
  failed: "Обработка моделью не завершена",
};
const reasonLabels: Record<string, string> = {
  not_installed: "Артефакты модели не установлены",
  not_configured: "Сервис VoiceFM не настроен",
  disabled: "Модель отключена оператором",
  weights_missing: "Отсутствуют разрешённые веса модели",
  missing_weights: "Отсутствуют разрешённые веса модели",
  manifest_missing: "Не установлен манифест артефактов",
  artifacts_missing: "Артефакты модели не установлены",
  service_unavailable: "Нет связи с сервисом модели",
  model_unavailable: "Модель недоступна",
  invalid_manifest: "Не удалось проверить манифест артефактов",
  inference_unavailable: "Обработка моделью недоступна",
};
const outputLabels: Record<string, string> = {
  cat_voice: "Голос",
  cat_neuro: "Неврологическая группа",
  cat_mood: "Настроение",
  cat_respiratory: "Дыхательная группа",
};
const demoWarning =
  "Демонстрационный режим — результат модели синтетический, не оценка состояния пациента.";
function DemoNotice() {
  return (
    <div className="demo-model-notice" role="note">
      <strong>{demoWarning}</strong>
      <p>
        Демонстрационные показатели заданы для примера и не вычислены по голосу
        пациента. Клинический контекст, дневники и акустические измерения ниже
        имеют собственные источники.
      </p>
    </div>
  );
}
function VoiceFMAvailability() {
  const query = useQuery<VoiceStatus>("/voicefm/status");
  return (
    <section className="voicefm-availability" aria-label="Состояние VoiceFM">
      <QueryFailure error={query.error} retry={query.reload} />
      {query.loading && !query.data ? (
        <p role="status">Проверяем режим модели…</p>
      ) : null}
      {query.data ? (
        query.data.is_mock || query.data.mode === "mock" ? (
          <DemoNotice />
        ) : (
          <>
            <strong>
              {query.data.ready
                ? "Исследовательский режим VoiceFM"
                : "VoiceFM недоступна"}
            </strong>
            {query.data.reason_code ? (
              <p>
                {reasonLabels[query.data.reason_code] ??
                  "Причина недоступности модели"}
                : <code>{query.data.reason_code}</code>
              </p>
            ) : null}
            <p className="helper">
              Выходы модели требуют проверки врачом; они не устанавливают
              диагноз и не назначают лечение.
            </p>
            {query.data.limitations?.map((item, i) => (
              <p className="helper" key={i}>
                {item}
              </p>
            ))}
          </>
        )
      ) : null}
    </section>
  );
}
function SnapshotFacts({ snapshot }: { snapshot: SourceSnapshot }) {
  const { audio, diaries } = snapshot;
  return (
    <>
      <h4>Данные записи и акустические измерения</h4>
      <p>
        {timeLabel(audio.recorded_at)} · {audio.device} ·{" "}
        {(
          {
            reading: "Чтение текста",
            free: "Свободная речь",
            vowel: "Протяжный звук",
          } as Record<string, string>
        )[audio.task] ?? audio.task}
      </p>
      <div className="table-scroll">
        <table>
          <caption>Сохранённые акустические измерения</caption>
          <thead>
            <tr>
              <th>Показатель</th>
              <th>Значение</th>
            </tr>
          </thead>
          <tbody>
            {Object.entries(metricLabels).map(
              ([key, [label, unit, factor]]) => (
                <tr key={key}>
                  <td>{label}</td>
                  <td>
                    {audio.result.metrics[key] == null
                      ? "Не измерено"
                      : `${(audio.result.metrics[key]! * factor).toLocaleString("ru-RU", { maximumFractionDigits: 2 })} ${unit}`}
                  </td>
                </tr>
              ),
            )}
          </tbody>
        </table>
      </div>
      <p className="helper">
        {audio.result.usable
          ? "Пригодность для акустического сравнения подтверждена обработчиком."
          : "Качество акустической записи ограничено."}{" "}
        Акустические значения сами по себе не являются диагнозом.
      </p>
      {snapshot.comparison ? (
        <details>
          <summary>Изменения относительно сопоставимой записи</summary>
          <ul>
            {Object.entries(snapshot.comparison.deltas).map(([key, delta]) => (
              <li key={key}>
                {metricLabels[key]?.[0] ?? key}:{" "}
                {(delta * (metricLabels[key]?.[2] ?? 1)).toLocaleString(
                  "ru-RU",
                  { maximumFractionDigits: 2, signDisplay: "exceptZero" },
                )}{" "}
                {key === "pause_ratio"
                  ? "п.п."
                  : (metricLabels[key]?.[1] ?? "")}
              </li>
            ))}
          </ul>
        </details>
      ) : (
        <p>Предыдущая сопоставимая запись отсутствует.</p>
      )}
      <h4>Клинический контекст в момент создания</h4>
      {snapshot.context_version ? (
        <>
          <p>
            Версия {snapshot.context_version.version} ·{" "}
            {snapshot.context_version.author_name} ·{" "}
            {snapshot.context_version.created_at
              ? timeLabel(snapshot.context_version.created_at)
              : "Дата не указана"}
          </p>
          <ContextFacts context={snapshot.context_version.context} />
        </>
      ) : (
        <p>
          Клинический контекст не был внесён. Пропуски не заменены
          предположениями.
        </p>
      )}
      <h4>Дневники в сохранённом периоде</h4>
      <p>
        {diaries.period_start} — {diaries.period_end} · {diaries.timezone}.
        Исходных записей: {diaries.source_entry_ids.length}.
      </p>
      <EMASummary summary={diaries.ema_summary} />
      <SleepSummary summary={diaries.csd_summary} />
      <details>
        <summary>Исходные ответы дневников ({diaries.entries.length})</summary>
        {diaries.entries.map((entry) => (
          <article className="clinical-version" key={entry.id}>
            <h5>
              {entry.kind === "ema" ? "Самочувствие" : "Сон"} ·{" "}
              {entry.local_date}
            </h5>
            <Answers entry={entry} />
            <small>
              Версия расчёта: {entry.scoring_version}; запись: {entry.id}
            </small>
          </article>
        ))}
      </details>
      <details className="clinical-source-list">
        <summary>Источники и версии отчёта</summary>
        <dl>
          <dt>Снимок данных</dt>
          <dd>{timeLabel(snapshot.captured_at)}</dd>
          <dt>Запись / успешная обработка</dt>
          <dd>
            {audio.recording_id} / {audio.run_id}
          </dd>
          <dt>Версия акустического анализа</dt>
          <dd>{audio.result.pipeline}</dd>
          <dt>SHA-256 аудиофайла</dt>
          <dd>{audio.sha256}</dd>
          <dt>Версия клинического контекста</dt>
          <dd>{snapshot.context_version?.id ?? "Не внесён"}</dd>
          <dt>Исходные дневники</dt>
          <dd>
            {diaries.source_entry_ids.length
              ? diaries.source_entry_ids.join(", ")
              : "Нет записей"}
          </dd>
        </dl>
      </details>
      {snapshot.limitations?.length ? (
        <section>
          <h4>Ограничения данных</h4>
          <ul>
            {snapshot.limitations.map((item, i) => (
              <li key={i}>{item}</li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}
function ModelResult({ report }: { report: ClinicalReport }) {
  const result = report.model_result;
  const mock =
    result?.is_mock ||
    result?.mode === "mock" ||
    report.source_snapshot.model_request?.is_mock ||
    report.source_snapshot.model_request?.mode === "mock";
  return (
    <section className="report-model-result">
      <h4>
        {mock
          ? "Демонстрационные выходы модели"
          : "Исследовательские выходы модели"}
      </h4>
      <p role="status">{modelStates[report.model_status]}</p>
      {report.model_reason ? (
        <p>
          {reasonLabels[report.model_reason] ?? "Состояние обработки"}:{" "}
          <code>{report.model_reason}</code>
        </p>
      ) : null}
      {report.model_status === "ready" && result ? (
        <>
          <p>
            {result.model?.name ?? "Модель"}
            {result.model?.revision ? ` · ${result.model.revision}` : ""}
          </p>
          {result.outputs?.scores?.length ? (
            <div className="table-scroll">
              <table>
                <caption>
                  {mock
                    ? "Синтетические значения для демонстрации интерфейса"
                    : "Сохранённые исследовательские значения; клиническая интерпретация не установлена"}
                </caption>
                <thead>
                  <tr>
                    <th>Выход</th>
                    <th>Значение</th>
                  </tr>
                </thead>
                <tbody>
                  {result.outputs.scores.map((score, i) => (
                    <tr key={i}>
                      <td>{outputLabels[score.label] ?? score.label}</td>
                      <td>
                        {Number.isFinite(score.score)
                          ? score.score.toLocaleString("ru-RU", {
                              maximumFractionDigits: 4,
                            })
                          : "Не указано"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p>Числовые выходы модели отсутствуют.</p>
          )}
          {mock ? (
            <p>
              Синтетические показатели заданы для демонстрации интерфейса и не
              зависят от записи. Их нельзя использовать для диагностики, оценки
              риска или выбора лечения.
            </p>
          ) : typeof result.outputs?.interpretation === "string" ? (
            <p>{result.outputs.interpretation}</p>
          ) : null}
          {mock ? (
            <p className="helper">
              Для применения реальной модели потребуются разрешённые артефакты и
              проверка пригодности для данных проекта.
            </p>
          ) : (
            result.limitations?.map((text, i) => (
              <p className="helper" key={i}>
                {text}
              </p>
            ))
          )}
          <details>
            <summary>Исходные данные модели и происхождение</summary>
            <pre className="clinical-model-json">
              {JSON.stringify(result, null, 2)}
            </pre>
          </details>
        </>
      ) : (
        <p className="helper">
          {report.model_status === "queued" ||
          report.model_status === "processing"
            ? "Состояние обновляется автоматически; снимок источников уже сохранён."
            : "Доступного результата модели нет. Сохранённые клинические и акустические факты остаются в отчёте."}
        </p>
      )}
    </section>
  );
}
function ReportCard({
  report,
  path,
}: {
  report: ClinicalReport;
  path: string;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const write = useClinicalWrite();
  useDraftGuard(Boolean(text.trim()), "Заключение врача");
  const mock =
    report.model_result?.is_mock ||
    report.model_result?.mode === "mock" ||
    report.source_snapshot.model_request?.is_mock ||
    report.source_snapshot.model_request?.mode === "mock";
  async function conclude(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await write(`${path}/${report.id}/conclusions`, { text: text.trim() });
      setText("");
      setNotice("Заключение врача сохранено с автором и датой.");
      refreshData();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <article
      className="clinical-report"
      data-testid={`clinical-report-${report.id}`}
    >
      <h3>
        {mock ? "Демонстрационный клинический отчёт" : "Клинический отчёт"} ·{" "}
        {timeLabel(report.created_at)}
      </h3>
      <p>Создал: {report.author_name ?? "лечащий врач"}.</p>
      {mock ? <DemoNotice /> : null}
      <p className="preserve-lines">{report.description}</p>
      <ModelResult report={report} />
      <details>
        <summary>Клинические, акустические и дневниковые источники</summary>
        <SnapshotFacts snapshot={report.source_snapshot} />
      </details>
      <section>
        <h4>Заключения врачей</h4>
        {report.conclusions?.length ? (
          report.conclusions.map((item) => (
            <article className="clinical-version" key={item.id}>
              <p className="preserve-lines">{item.text}</p>
              <small>
                {item.author_name} · {timeLabel(item.created_at)}
              </small>
            </article>
          ))
        ) : (
          <p>Врачебное заключение пока не записано.</p>
        )}
        <form onSubmit={conclude}>
          <label>
            Авторское заключение врача
            <textarea
              value={text}
              maxLength={10000}
              rows={3}
              required
              disabled={busy}
              onChange={(e) => setText(e.target.value)}
            />
          </label>
          <p className="helper">
            Сохранится отдельной записью от вашего имени. Ранее сохранённые
            заключения не перезаписываются.
          </p>
          <ErrorBox message={error} />
          {notice ? <Notice>{notice}</Notice> : null}
          <button className="primary" disabled={busy || !text.trim()}>
            {busy ? "Сохраняем…" : "Сохранить заключение врача"}
          </button>
        </form>
      </section>
    </article>
  );
}
function ReportList({
  patientID,
  recordingID,
}: {
  patientID: string;
  recordingID: string;
}) {
  const path = `/patients/${patientID}/audio/${recordingID}/clinical-reports`;
  const page = usePages<ClinicalReport>(path);
  const write = useClinicalWrite();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<ClinicalReport | null>(null);
  async function generate() {
    setBusy(true);
    setError("");
    try {
      const report = await write<ClinicalReport>(path, {});
      setCreated(report);
      refreshData();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const reports = page.accessDenied
    ? []
    : created && !page.items.some((item) => item.id === created.id)
      ? [created, ...page.items]
      : page.items;
  return (
    <section>
      <VoiceFMAvailability />
      <p className="helper">
        Отчёт фиксирует текущую версию клинического контекста, успешную
        обработку аудио и исходные дневники. Врачебное заключение сохраняется
        отдельно.
      </p>
      <ErrorBox message={error} />
      <button
        className="secondary"
        disabled={busy || page.accessDenied}
        onClick={() => void generate()}
      >
        {busy ? "Создаём снимок…" : "Создать клинический отчёт"}
      </button>
      <QueryFailure error={page.error} retry={page.reload} />
      {page.loading && !reports.length ? <Loading /> : null}
      {reports.map((report) => (
        <ReportCard key={report.id} report={report} path={path} />
      ))}
      {!page.loading && !reports.length && !page.error ? (
        <p>Клинических отчётов по этой записи пока нет.</p>
      ) : null}
      <PageControls
        total={Math.max(page.total, reports.length)}
        shown={reports.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
    </section>
  );
}
export function AudioClinicalReports({
  patientID,
  recordingID,
}: {
  patientID: string;
  recordingID: string;
}) {
  const { user } = useSession();
  const [open, setOpen] = useState(false);
  if (user?.role !== "doctor") return null;
  return (
    <section className="audio-clinical-reports">
      <button
        className="text-button"
        aria-expanded={open}
        onClick={() => {
          if (!open || confirmDiscard()) setOpen(!open);
        }}
      >
        Клинические отчёты по записи
      </button>
      {open ? (
        <ReportList patientID={patientID} recordingID={recordingID} />
      ) : null}
    </section>
  );
}
