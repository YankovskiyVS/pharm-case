import type { Model } from "../api-client";
import { shortDate } from "./common";
function value(v: number | null | undefined, digits = 1) {
  return v == null
    ? "—"
    : v.toLocaleString("ru-RU", { maximumFractionDigits: digits });
}
export function EMASummary({
  summary: s,
  clinical = false,
}: {
  summary: Model<"Summary">;
  clinical?: boolean;
}) {
  const metrics = [
    ["Настроение", s.mood],
    ["Отсутствие интереса", s.anhedonia],
    ["Энергия", s.energy],
    ["Тревога", s.anxiety],
    ["Максимальная тревога", s.max_anxiety],
    ["Минимальная энергия", s.min_energy],
  ] as const;
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>EMA за три дня</h2>
        <span className="muted">Заполнено {s.count} из 3 дней</span>
      </div>
      <div className="summary-grid">
        {metrics.map(([label, n]) => (
          <div key={label}>
            <span>{label}</span>
            <strong>
              {value(n)}
              <small> / 10</small>
            </strong>
          </div>
        ))}
      </div>
      <div className="summary-notes">
        <p>
          Изменение настроения, день 3 − день 1:{" "}
          <strong>
            {s.mood_delta == null
              ? "Недостаточно данных"
              : `${s.mood_delta > 0 ? "+" : ""}${s.mood_delta}`}
          </strong>
        </p>
        <p>
          Дней с ответом «Да» о суицидальных мыслях:{" "}
          <strong>{s.count ? s.suicidal_days : "Нет данных"}</strong>
        </p>
        {clinical && s.suicidal_days > 0 ? (
          <p className="summary-signal danger">
            Красный флаг по EMA: связаться с пациентом сегодня. Проверьте
            сигналы и зафиксируйте действие.
          </p>
        ) : null}
        {clinical && s.mood_delta != null && s.mood_delta >= 3 ? (
          <p className="summary-signal warning">
            Сигнал ухудшения по шаблону EMA: рост оценки настроения на 3 балла
            или более. Требуется оценка врача.
          </p>
        ) : null}
        <p className="helper">
          {s.complete
            ? "Период заполнен полностью."
            : "Средние рассчитаны только по заполненным дням."}{" "}
          Выше настроение/ангедония/тревога — хуже; выше энергия — лучше.
        </p>
      </div>
    </section>
  );
}
export function SleepSummary({
  summary: s,
}: {
  summary: Model<"SleepSummary">;
}) {
  const rows: [string, number | null | undefined, string, string][] = [
    ["Латентность засыпания", s.mean_latency, "мин", "latency"],
    ["Число пробуждений", s.mean_awakenings, "", "awakenings"],
    ["Бодрствование ночью (WASO)", s.mean_waso, "мин", "waso"],
    ["Общее время сна", s.mean_total_sleep, "мин", "total_sleep"],
    [
      "Эффективность сна",
      s.mean_se == null ? null : s.mean_se * 100,
      "%",
      "se",
    ],
    ["Качество сна", s.mean_quality, "/ 5", "quality"],
    ["Бодрость после сна", s.mean_rested, "/ 5", "rested"],
    ["Дневная дремота", s.mean_nap_minutes, "мин", "nap_minutes"],
    ["Алкоголь, сумма", s.total_alcohol, "порций", "alcohol"],
    ["Кофеин, сумма", s.total_caffeine, "порций", "caffeine"],
  ];
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>Сводка сна за 14 дней</h2>
        <span className="muted">
          {shortDate(s.from)} — {shortDate(s.to)}
        </span>
      </div>
      <p className="helper">
        Заполнено {s.count} из 14 дней. Средние учитывают только заполненные
        поля; алкоголь и кофеин суммируются. Это не суммарный балл и не диагноз.
      </p>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Показатель</th>
              <th>Значение</th>
              <th>Дней с данными</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(([label, n, unit, key]) => (
              <tr key={key}>
                <td>{label}</td>
                <td>
                  {(s.samples[key] ?? 0) > 0
                    ? `${value(n)} ${unit}`
                    : "Не указано"}
                </td>
                <td>{s.samples[key] ?? 0}/14</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
