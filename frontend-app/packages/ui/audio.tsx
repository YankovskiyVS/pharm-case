import { metricLabels } from "./audio-metrics";
import { AudioClinicalReports } from "./clinical-reports";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, refreshData, uploadAudio, type Model } from "../api-client";
import { useQuery } from "./session";
import { Empty, ErrorBox, Loading, shortDate } from "./common";
import { navigate, useDraftGuard, useUrlState } from "./navigation";
import { usePages, PageControls, QueryFailure } from "./pagination";

type Recording = Model<"AudioRecording">;
type SearchResult = Model<"AudioSearchResult">;

const tasks: Record<string, string> = {
  reading: "Чтение одинакового текста",
  free: "Рассказ о самочувствии",
  vowel: "Протяжный звук «а»",
};
const prompts: Record<string, string> = {
  reading:
    "Прочитайте спокойно: «Утром я открываю окно и смотрю на улицу. За окном начинается обычный день. Я могу сделать небольшую паузу, спокойно вдохнуть и продолжить свои дела. Сегодня я расскажу о том, как спал и как себя чувствую». Для сравнения каждый раз читайте этот же текст.",
  free: "Расскажите за 20–60 секунд, как вы спали и как чувствуете себя сегодня. Говорите в обычном темпе.",
  vowel:
    "После обычного вдоха произнесите звук «а» ровно и без усилия около 5 секунд. Остановитесь, если некомфортно.",
};
const warnings: Record<string, string> = {
  low_activity_contrast:
    "Низкий контраст речи и фона: возможен шум или неточное выделение речи",
  short_recording: "Запись слишком короткая для сравнения",
  clipping: "Сигнал перегружен: отодвиньте микрофон",
  low_level: "Слишком тихая запись",
  insufficient_voice: "Недостаточно пригодных участков голоса",
  insufficient_periodic_voice: "Основной тон не удалось надёжно измерить",
};
const transcriptionStates: Record<string, string> = {
  ready: "Расшифровка готова",
  failed:
    "Расшифровка недоступна. Измерения голоса сохранены; можно повторить обработку.",
  insufficient_audio: "Для расшифровки недостаточно качественного аудио",
  no_confident_speech: "Не удалось уверенно распознать слова",
  not_applicable: "Для протяжного звука расшифровка не требуется",
};

function stamp(seconds: number) {
  return `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2, "0")}`;
}

function uploadedAudioType(file: File): string | null {
  const aliases: Record<string, string> = {
    "audio/wav": "audio/wav",
    "audio/x-wav": "audio/wav",
    "audio/wave": "audio/wav",
    "audio/vnd.wave": "audio/wav",
    "audio/webm": "audio/webm",
    "audio/ogg": "audio/ogg",
    "application/ogg": "audio/ogg",
    "audio/mp4": "audio/mp4",
    "audio/x-m4a": "audio/mp4",
    "audio/m4a": "audio/mp4",
    "audio/mpeg": "audio/mpeg",
    "audio/mp3": "audio/mpeg",
  };
  const supplied = file.type.toLowerCase().split(";")[0].trim();
  if (aliases[supplied]) return aliases[supplied];
  if (supplied && supplied !== "application/octet-stream") return null;
  const extensions: Record<string, string> = {
    wav: "audio/wav",
    webm: "audio/webm",
    ogg: "audio/ogg",
    m4a: "audio/mp4",
    mp4: "audio/mp4",
    mp3: "audio/mpeg",
  };
  return extensions[file.name.split(".").pop()?.toLowerCase() ?? ""] ?? null;
}

function requestMicrophone(signal: AbortSignal): Promise<MediaStream> {
  return new Promise((resolve, reject) => {
    let finished = false;
    const finishError = (error: Error) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      reject(error);
    };
    const abort = () =>
      finishError(
        new DOMException(
          "Запрос доступа к микрофону отменён. Можно загрузить аудиофайл.",
          "AbortError",
        ),
      );
    const timer = setTimeout(
      () =>
        finishError(
          new Error(
            "Не удалось получить доступ к микрофону за 15 секунд. Проверьте разрешение браузера или загрузите аудиофайл.",
          ),
        ),
      15000,
    );
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) {
      abort();
      return;
    }
    navigator.mediaDevices
      .getUserMedia({
        audio: {
          echoCancellation: false,
          noiseSuppression: false,
          autoGainControl: false,
        },
      })
      .then((media) => {
        if (finished) {
          media.getTracks().forEach((track) => track.stop());
          return;
        }
        finished = true;
        clearTimeout(timer);
        signal.removeEventListener("abort", abort);
        resolve(media);
      }, finishError);
  });
}

function AudioCapture() {
  const [task, setTask] = useState("reading");
  const [device, setDevice] = useState("");
  const [consent, setConsent] = useState(false);
  const [blob, setBlob] = useState<Blob | null>(null);
  const [url, setURL] = useState("");
  const [previewError, setPreviewError] = useState(false);
  const [recording, setRecording] = useState(false);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState(0);
  const uploadController = useRef<AbortController | null>(null);
  const microphoneRequest = useRef<AbortController | null>(null);
  const [starting, setStarting] = useState(false);
  const [seconds, setSeconds] = useState(0);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const recorder = useRef<MediaRecorder | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const mounted = useRef(true);
  const key = useRef(crypto.randomUUID());
  const timer = useRef<ReturnType<typeof setInterval> | null>(null);
  const limit = useRef<ReturnType<typeof setTimeout> | null>(null);

  function cleanup() {
    if (timer.current) clearInterval(timer.current);
    if (limit.current) clearTimeout(limit.current);
    stream.current?.getTracks().forEach((t) => t.stop());
    stream.current = null;
  }
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      microphoneRequest.current?.abort();
      if (recorder.current?.state === "recording") recorder.current.stop();
      cleanup();
    };
  }, []);
  useEffect(() => {
    if (!blob) {
      setURL("");
      return;
    }
    const object = URL.createObjectURL(blob);
    setURL(object);
    return () => URL.revokeObjectURL(object);
  }, [blob]);
  function selectBlob(value: Blob | null) {
    setBlob(value);
    setPreviewError(false);
    key.current = crypto.randomUUID();
    setSaved("");
  }
  async function start() {
    setError("");
    setStarting(true);
    const controller = new AbortController();
    microphoneRequest.current = controller;
    try {
      if (
        !navigator.mediaDevices?.getUserMedia ||
        typeof MediaRecorder === "undefined"
      )
        throw new Error(
          "Запись в этом браузере недоступна. Загрузите аудиофайл.",
        );
      const media = await requestMicrophone(controller.signal);
      if (!mounted.current || controller.signal.aborted) {
        media.getTracks().forEach((track) => track.stop());
        return;
      }
      stream.current = media;
      const mime = [
        "audio/webm;codecs=opus",
        "audio/mp4",
        "audio/ogg;codecs=opus",
      ].find((m) => MediaRecorder.isTypeSupported(m));
      const rec = new MediaRecorder(
        media,
        mime ? { mimeType: mime } : undefined,
      );
      recorder.current = rec;
      const parts: Blob[] = [];
      rec.ondataavailable = (e) => {
        if (e.data.size) parts.push(e.data);
      };
      rec.onstop = () => {
        cleanup();
        if (mounted.current) {
          selectBlob(new Blob(parts, { type: rec.mimeType }));
          setRecording(false);
        }
      };
      rec.onerror = () => {
        cleanup();
        if (mounted.current) {
          setRecording(false);
          setError("Запись прервана. Повторите попытку.");
        }
      };
      rec.start(1000);
      selectBlob(null);
      setSeconds(0);
      setRecording(true);
      timer.current = setInterval(() => setSeconds((s) => s + 1), 1000);
      limit.current = setTimeout(() => {
        if (rec.state === "recording") rec.stop();
      }, 115000);
    } catch (e) {
      cleanup();
      if (mounted.current)
        setError((e as Error).message || "Нет доступа к микрофону");
    } finally {
      if (microphoneRequest.current === controller) {
        microphoneRequest.current = null;
        if (mounted.current) setStarting(false);
      }
    }
  }
  useDraftGuard(Boolean(blob) || recording || starting, "Аудиозапись");
  useEffect(() => () => uploadController.current?.abort(), []);
  async function submit() {
    if (!blob || !consent) return;
    if (blob.size > 12 * 1024 * 1024) {
      setError("Максимальный размер — 12 МБ");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const controller = new AbortController();
      uploadController.current = controller;
      setProgress(0);
      await uploadAudio(blob, task, device.trim(), key.current, {
        signal: controller.signal,
        onProgress: setProgress,
      });
      selectBlob(null);
      setSaved("Запись сохранена. Анализ выполняется в фоне.");
      refreshData();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="panel audio-capture">
      <h2>Записать голос</h2>
      <p>
        Тихое помещение, обычный голос, одно и то же расстояние до микрофона. От
        5 секунд до 2 минут, до 12 МБ. В записи должен говорить только пациент.
      </p>
      <label>
        Задание
        <select
          aria-label="Задание"
          value={task}
          disabled={recording || busy || starting}
          onChange={(e) => {
            setTask(e.target.value);
            key.current = crypto.randomUUID();
            setSaved("");
          }}
        >
          {Object.entries(tasks).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
      </label>
      <p className="audio-prompt">{prompts[task]}</p>
      <label>
        Устройство и микрофон
        <input
          value={device}
          maxLength={120}
          disabled={recording || busy || starting}
          placeholder="Например: мой телефон, встроенный микрофон"
          onChange={(e) => {
            setDevice(e.target.value);
            key.current = crypto.randomUUID();
            setSaved("");
          }}
        />
      </label>
      <small>
        Повторяйте это обозначение только для того же устройства и условий
        записи. У загруженного файла укажите устройство, на котором его
        записали.
      </small>
      <label className="audio-consent">
        <input
          type="checkbox"
          checked={consent}
          onChange={(e) => setConsent(e.target.checked)}
          disabled={recording || busy || starting}
        />
        Я согласен на сохранение и автоматическую обработку этой записи голоса и
        предоставление записи и результатов моим лечащим врачам.
      </label>
      <div className="audio-actions">
        {recording ? (
          <button className="primary" onClick={() => recorder.current?.stop()}>
            Остановить запись · {stamp(seconds)}
          </button>
        ) : (
          <button
            className="secondary"
            disabled={!consent || !device.trim() || busy || starting}
            onClick={() => void start()}
          >
            {starting ? "Доступ к микрофону…" : "Начать запись"}
          </button>
        )}
        {starting ? (
          <button
            className="secondary"
            onClick={() => microphoneRequest.current?.abort()}
          >
            Отменить доступ к микрофону
          </button>
        ) : null}
        <label>
          Или загрузить аудиофайл
          <input
            type="file"
            accept="audio/wav,audio/x-wav,audio/wave,audio/vnd.wave,audio/webm,audio/ogg,application/ogg,audio/mp4,audio/x-m4a,audio/m4a,audio/mpeg,audio/mp3,.wav,.webm,.ogg,.m4a,.mp4,.mp3"
            disabled={recording || busy || starting}
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) {
                const type = uploadedAudioType(file);
                if (type) {
                  selectBlob(new Blob([file], { type }));
                  setError("");
                } else {
                  setError(
                    "Формат файла не поддерживается. Выберите WAV, WebM, Ogg, M4A/MP4 или MP3.",
                  );
                }
              }
              e.target.value = "";
            }}
          />
        </label>
      </div>
      {blob && (!device.trim() || !consent) ? (
        <p className="helper" role="status">
          Для отправки{!device.trim() ? " укажите устройство и микрофон" : ""}
          {!device.trim() && !consent ? " и" : ""}
          {!consent ? " подтвердите согласие на обработку записи" : ""}.
        </p>
      ) : null}
      {url ? (
        <div className="audio-preview">
          <audio
            controls
            src={url}
            aria-label="Прослушать перед отправкой"
            onError={() => setPreviewError(true)}
          />
          {previewError ? (
            <p className="helper" role="status">
              Браузер не смог воспроизвести этот файл. Его можно отправить на
              анализ; предварительное прослушивание не требуется.
            </p>
          ) : null}
          <button
            className="primary"
            disabled={busy || !consent || !device.trim()}
            onClick={() => void submit()}
          >
            {busy ? `Отправка… ${progress}%` : "Отправить на анализ"}
          </button>
          {busy ? (
            <button
              className="secondary"
              onClick={() => uploadController.current?.abort()}
            >
              Отменить загрузку
            </button>
          ) : null}
          <button
            className="text-button"
            disabled={busy}
            onClick={() => selectBlob(null)}
          >
            Удалить черновик
          </button>
        </div>
      ) : null}
      <ErrorBox message={error} />
      {saved ? <p role="status">{saved}</p> : null}
      <p className="helper">
        Черновик хранится только на этой странице до отправки. Анализ не
        является медицинским заключением.
      </p>
    </section>
  );
}

function AudioRecord({
  item,
  patientID,
}: {
  item: Recording;
  patientID: string;
}) {
  const player = useRef<HTMLAudioElement | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const base = `/patients/${patientID}/audio/${item.id}`;
  const state = {
    queued: "В очереди",
    processing: "Обрабатывается",
    ready: "Обработано",
    failed: "Не удалось обработать",
  }[item.status];
  async function retry() {
    setBusy(true);
    setError("");
    try {
      await api(`${base}/retry`, {});
      refreshData();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const result = item.result;
  const extended = item;
  return (
    <article className="panel audio-record" id={`audio-${item.id}`}>
      <div className="section-heading">
        <h3>{tasks[item.task]}</h3>
        <span className="badge neutral">{state}</span>
      </div>
      <p className="muted">
        {shortDate(item.created_at)} · {item.device}
      </p>
      <audio
        ref={player}
        controls
        preload="none"
        src={`/api/v1${base}/file`}
        aria-label={`Аудиозапись ${shortDate(item.created_at)}`}
      />
      {item.error_code ? (
        <p role="status">
          {item.error_code === "invalid_audio"
            ? "Файл повреждён или длительность вне диапазона 2–120 секунд."
            : "Сервис обработки временно недоступен. При повторных сбоях можно запустить анализ снова."}
        </p>
      ) : null}
      {result &&
      extended.result_run_id &&
      extended.result_run_id !== item.run_id ? (
        <p className="notice" role="status">
          Показан сохранённый результат предыдущей успешной обработки. Текущая
          попытка ещё не дала нового результата.
        </p>
      ) : null}
      <AudioRuns patientID={patientID} recordingID={item.id} />
      <AudioClinicalReports patientID={patientID} recordingID={item.id} />
      {result ? (
        <>
          {result.warnings.map((code) => (
            <p className="helper" key={code}>
              {warnings[code] || code}
            </p>
          ))}
          <p>
            {result.usable
              ? "Запись пригодна для акустического сравнения при сопоставимых условиях."
              : "Качество ограничено. Автоматическое сравнение отключено."}
          </p>
          <div className="audio-metrics">
            {Object.entries(metricLabels).map(([key, [label, unit, scale]]) => {
              const value = result.metrics[key];
              return (
                <div key={key}>
                  <span>{label}</span>
                  <strong>
                    {value == null ? "—" : (value * scale).toFixed(1)}{" "}
                    <small>{value == null ? "" : unit}</small>
                  </strong>
                </div>
              );
            })}
          </div>
          {item.comparison ? (
            <div className="audio-comparison">
              <h4>
                Изменения относительно{" "}
                <a
                  href={`#audio-${item.comparison.recording_id}`}
                  onClick={(event) => {
                    event.preventDefault();
                    navigate({
                      target_type: "audio",
                      target: item.comparison!.recording_id,
                      anchor: `audio-${item.comparison!.recording_id}`,
                    });
                  }}
                >
                  предыдущей сопоставимой записи
                </a>
              </h4>
              <p className="helper">
                То же задание, обозначение устройства и версия анализа. Это
                сравнение измерений, а не оценка заболевания.
              </p>
              <ul>
                {Object.entries(item.comparison.deltas).map(([key, value]) => {
                  const [label, unit, scale] = metricLabels[key];
                  return (
                    <li key={key}>
                      {label}: {value > 0 ? "+" : ""}
                      {(value * scale).toFixed(1)}{" "}
                      {key === "pause_ratio" ? "п.п." : unit}
                    </li>
                  );
                })}
              </ul>
            </div>
          ) : (
            <p className="helper">Предыдущей сопоставимой записи пока нет.</p>
          )}
          <p>
            {transcriptionStates[result.transcription_status] ||
              "Расшифровка недоступна"}
          </p>
          <ol className="audio-transcript">
            {result.segments.map((segment, i) => (
              <li key={`${item.run_id}-${i}`}>
                <button
                  className="text-button"
                  onClick={() => {
                    if (player.current) {
                      player.current.currentTime = segment.start;
                      void player.current
                        .play()
                        .catch(() =>
                          setError("Не удалось воспроизвести запись"),
                        );
                    }
                  }}
                >
                  {stamp(segment.start)}–{stamp(segment.end)}
                </button>{" "}
                {segment.text}
              </li>
            ))}
          </ol>
          <small className="muted">
            Версия анализа: {result.pipeline}. Уровень dBFS зависит от усиления
            микрофона. Проверяйте, что в записи говорит пациент.
          </small>
        </>
      ) : null}
      {item.status === "failed" ||
      (item.status === "ready" && result?.transcription_status === "failed") ? (
        <button
          className="secondary"
          disabled={busy}
          onClick={() => void retry()}
        >
          Повторить обработку
        </button>
      ) : null}
      <ErrorBox message={error} />
    </article>
  );
}

function AudioSearch({ patientID }: { patientID: string }) {
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<SearchResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setResult(null);
    try {
      setResult(
        await api<SearchResult>(`/patients/${patientID}/audio/search`, {
          query,
        }),
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="panel">
      <h2>Поиск по аудиозаписям</h2>
      <form className="audio-search" onSubmit={(e) => void submit(e)}>
        <label>
          Вопрос по записям пациента
          <input
            value={query}
            minLength={2}
            maxLength={1000}
            required
            placeholder="Что пациент говорил о сне?"
            onChange={(e) => setQuery(e.target.value)}
          />
        </label>
        <button className="primary" disabled={busy}>
          {busy ? "Поиск…" : "Найти фрагменты"}
        </button>
      </form>
      <ErrorBox message={error} />
      {result ? (
        <div role="status">
          <p className="audio-answer">{result.answer}</p>
          <ol>
            {result.sources.map((s) => (
              <li key={s.id}>
                <a
                  href={`#audio-${s.recording_id}`}
                  onClick={(event) => {
                    event.preventDefault();
                    navigate({
                      target_type: "audio",
                      target: s.recording_id,
                      anchor: `audio-${s.recording_id}`,
                    });
                  }}
                >
                  Открыть запись
                </a>{" "}
                · {stamp(s.start)}–{stamp(s.end)}
                <audio
                  controls
                  preload="none"
                  src={`/api/v1/patients/${patientID}/audio/${s.recording_id}/file#t=${s.start},${s.end}`}
                  aria-label={`Источник ${stamp(s.start)}`}
                />
              </li>
            ))}
          </ol>
          <p className="helper">{result.limitations}</p>
          {result.truncated ? (
            <p className="helper">
              Поиск ограничен 500 последними фрагментами.
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

function VoiceTrend({ records }: { records: Recording[] }) {
  const reference = records.find(
    (r) =>
      r.status === "ready" &&
      r.result?.usable &&
      r.result.metrics.f0_median_hz != null,
  );
  if (!reference) return null;
  const compatible = records
    .filter(
      (r) =>
        r.status === "ready" &&
        r.result?.usable &&
        r.task === reference.task &&
        r.device === reference.device &&
        r.result.pipeline === reference.result?.pipeline &&
        r.result.metrics.f0_median_hz != null,
    )
    .slice(0, 14)
    .reverse();
  if (compatible.length < 2) return null;
  const values = compatible.map((r) => r.result!.metrics.f0_median_hz!);
  const low = Math.min(...values) - 5;
  const high = Math.max(...values) + 5;
  const points = values.map(
    (value, i) =>
      `${40 + (i * 520) / (values.length - 1)},${150 - ((value - low) * 120) / (high - low)}`,
  );
  return (
    <section className="panel">
      <h3>Динамика основного тона</h3>
      <p className="helper">
        {tasks[reference.task]} · {reference.device}. До 14 сопоставимых
        записей; изменения не обозначают клиническое ухудшение.
      </p>
      <svg
        viewBox="0 0 600 180"
        role="img"
        aria-label="График медианы основного тона, значения в таблице ниже"
        style={{ width: "100%", maxHeight: 220 }}
      >
        <line x1="40" y1="150" x2="560" y2="150" stroke="#d5ddd5" />
        <polyline
          points={points.join(" ")}
          fill="none"
          stroke="#527562"
          strokeWidth="3"
        />
        {points.map((point, i) => {
          const [x, y] = point.split(",");
          return (
            <circle key={compatible[i].id} cx={x} cy={y} r="4" fill="#527562">
              <title>
                {shortDate(compatible[i].created_at)}: {values[i].toFixed(1)} Гц
              </title>
            </circle>
          );
        })}
      </svg>
      <div className="table-scroll">
        <table>
          <caption>Измеренные значения основного тона</caption>
          <thead>
            <tr>
              <th>Дата</th>
              <th>Медиана, Гц</th>
            </tr>
          </thead>
          <tbody>
            {compatible.map((r) => (
              <tr key={r.id}>
                <td>
                  <a href={`#audio-${r.id}`}>{shortDate(r.created_at)}</a>
                </td>
                <td>{r.result!.metrics.f0_median_hz!.toFixed(1)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export function AudioWorkspace({
  patientID,
  capture = false,
}: {
  patientID: string;
  capture?: boolean;
}) {
  const page = usePages<Recording>(
    `/patients/${patientID}/audio`,
    false,
    "recordings",
  );
  const data = { recordings: page.items },
    error = page.error,
    loading = page.loading;
  const [target] = useUrlState("target", "");
  const [targetType] = useUrlState("target_type", "");
  const direct = useQuery<Recording>(
    target && targetType === "audio"
      ? `/patients/${patientID}/audio/${target}`
      : null,
  );
  const records =
    direct.data && !page.items.some((item) => item.id === direct.data?.id)
      ? [direct.data, ...page.items]
      : page.items;
  useEffect(() => {
    const id = location.hash.slice(1);
    if (records.length && id.startsWith("audio-"))
      document.getElementById(id)?.scrollIntoView({ block: "center" });
  }, [target, records.length]);
  return (
    <div className="audio-workspace">
      <header className="page-heading">
        <div>
          <h2>Голос пациента</h2>
          <p className="muted">
            Записи, акустические измерения и динамика. Не является медицинским
            заключением.
          </p>
        </div>
      </header>
      <ModelReadiness />
      {capture ? <AudioCapture /> : <AudioSearch patientID={patientID} />}
      <QueryFailure error={error || direct.error} retry={page.reload} />
      {records.length ? <VoiceTrend records={records} /> : null}
      {loading ? <Loading /> : null}
      {!loading && !error && data.recordings.length === 0 ? (
        <Empty>Аудиозаписей пока нет.</Empty>
      ) : null}
      {records.map((item) => (
        <AudioRecord key={item.id} item={item} patientID={patientID} />
      ))}
      <PageControls
        total={page.total}
        shown={page.items.length}
        cursor={page.cursor}
        loading={page.moreLoading}
        onMore={() => void page.more()}
      />
    </div>
  );
}

function ModelReadiness() {
  const { data, error, reload } = useQuery<{
    status: string;
    models?: Record<string, { state: string; error_code?: string }>;
  }>("/audio/status");
  if (error)
    return (
      <section className="panel">
        <ErrorBox message="Не удалось проверить готовность моделей. Записи сохраняются отдельно от обработки." />
        <button className="secondary" onClick={reload}>
          Проверить готовность
        </button>
      </section>
    );
  if (!data) return <p role="status">Проверяем готовность обработки…</p>;
  if (data.status === "ready")
    return (
      <p className="helper" role="status">
        Модели готовы к локальной обработке.
      </p>
    );
  return (
    <section className="panel" role="status">
      <h3>
        {data.status === "warming"
          ? "Модели подготавливаются"
          : "Обработка готова частично"}
      </h3>
      <p>
        Первый запуск требует загрузки моделей. Запись можно сохранить;
        распознавание и поиск станут доступны после подготовки. Не отправляйте
        запись повторно: проверьте статус и при необходимости повторите
        обработку.
      </p>
      <ul>
        {Object.entries(data.models ?? {}).map(([name, model]) => (
          <li key={name}>
            {name === "asr" ? "Распознавание речи" : "Поиск по тексту"}:{" "}
            {model.state === "ready"
              ? "готово"
              : model.state === "loading"
                ? "подготавливается"
                : "пока недоступно"}
          </li>
        ))}
      </ul>
      <button className="secondary" onClick={reload}>
        Обновить статус
      </button>
    </section>
  );
}

function AudioRuns({
  patientID,
  recordingID,
}: {
  patientID: string;
  recordingID: string;
}) {
  const [open, setOpen] = useState(false);
  const page = usePages<Model<"AudioRun">>(
    open ? `/patients/${patientID}/audio/${recordingID}/runs` : null,
  );
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>История обработки</summary>
      <QueryFailure error={page.error} retry={page.reload} />
      {page.loading ? <p>Загружаем историю…</p> : null}
      {page.items.map((run) => (
        <article className="review-row" key={run.id}>
          <div>
            <strong>
              {
                (
                  {
                    queued: "В очереди",
                    processing: "Выполняется",
                    ready: "Успешно",
                    failed: "Не удалось обработать",
                  } as Record<string, string>
                )[run.status]
              }
            </strong>
            <small>
              {shortDate(run.created_at)}
              {run.pipeline ? ` — ${run.pipeline}` : ""}
            </small>
            {run.error_code ? (
              <p>
                Результат этой попытки недоступен. Сохранённые успешные
                измерения остаются в карточке записи.
              </p>
            ) : null}
            {run.result ? (
              <details>
                <summary>Измерения этой попытки</summary>
                <dl>
                  {Object.entries(metricLabels).map(
                    ([key, [label, unit, scale]]) => (
                      <div key={key}>
                        <dt>{label}</dt>
                        <dd>
                          {run.result?.metrics[key] == null
                            ? "Не измерено"
                            : `${(run.result.metrics[key]! * scale).toFixed(1)} ${unit}`}
                        </dd>
                      </div>
                    ),
                  )}
                </dl>
              </details>
            ) : null}
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
    </details>
  );
}
